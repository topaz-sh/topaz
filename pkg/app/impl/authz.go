package impl

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/aserto-dev/aserto-grpc/grpcutil"
	api_v2 "github.com/aserto-dev/go-authorizer/aserto/api/v2"
	authz2 "github.com/aserto-dev/go-authorizer/aserto/authorizer/v2"
	authz "github.com/aserto-dev/go-grpc-authz/aserto/authorizer/authorizer/v1"
	api "github.com/aserto-dev/go-grpc/aserto/api/v1"
	"github.com/aserto-dev/go-utils/cerr"
	"github.com/aserto-dev/go-utils/pb"
	runtime "github.com/aserto-dev/runtime"
	decisionlog_plugin "github.com/aserto-dev/topaz/decision_log/plugin"
	"github.com/aserto-dev/topaz/pkg/cc/config"
	"github.com/aserto-dev/topaz/resolvers"
	"github.com/google/uuid"
	"github.com/open-policy-agent/opa/rego"
	"github.com/open-policy-agent/opa/server/types"
	"github.com/pkg/errors"
	"github.com/rs/zerolog"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	InputUser     string = "user"
	InputIdentity string = "identity"
	InputPolicy   string = "policy"
	InputResource string = "resource"
)

type AuthorizerServer struct {
	cfg    *config.Common
	logger *zerolog.Logger

	runtimeResolver   resolvers.RuntimeResolver
	directoryResolver resolvers.DirectoryResolver
}

func NewAuthorizerServer(
	logger *zerolog.Logger,
	cfg *config.Common,
	runtimeResolver resolvers.RuntimeResolver,
	directoryResolver resolvers.DirectoryResolver) *AuthorizerServer {

	newLogger := logger.With().Str("component", "api.grpc").Logger()
	return &AuthorizerServer{
		cfg:               cfg,
		runtimeResolver:   runtimeResolver,
		logger:            &newLogger,
		directoryResolver: directoryResolver,
	}
}

func (s *AuthorizerServer) DecisionTree(ctx context.Context, req *authz2.DecisionTreeRequest) (*authz2.DecisionTreeResponse, error) { // nolint:funlen,gocyclo //TODO: split into smaller functions after merge with onebox
	log := grpcutil.CompleteLogger(ctx, s.logger)

	resp := &authz2.DecisionTreeResponse{}

	policyContext := getPolicyInfoFromContext(ctx)

	if req.ResourceContext == nil {
		req.ResourceContext = &structpb.Struct{Fields: make(map[string]*structpb.Value)}
	}

	if req.Options == nil {
		req.Options = &authz2.DecisionTreeOptions{
			PathSeparator: authz2.PathSeparator_PATH_SEPARATOR_DOT,
		}
	}

	if req.IdentityContext == nil {
		return resp, cerr.ErrInvalidArgument.Msg("identity context not set")
	}

	if req.IdentityContext.Type == api.IdentityType_IDENTITY_TYPE_UNKNOWN {
		return resp, cerr.ErrInvalidArgument.Msg("identity type UNKNOWN")
	}

	user, err := s.getUserFromIdentityContext(ctx, req.IdentityContext)
	if err != nil {
		log.Error().Err(err).Interface("req", req).Msg("failed to resolve identity context")
		return resp, cerr.ErrAuthenticationFailed.WithGRPCStatus(codes.NotFound).Msg("failed to resolve identity context")
	}

	input := map[string]interface{}{
		InputUser:     convert(user),
		InputIdentity: convert(req.IdentityContext),
		InputPolicy:   policyContext,
		InputResource: req.ResourceContext,
	}

	policyRuntime, err := s.runtimeResolver.RuntimeFromContext(ctx, policyContext.GetId(), policyContext.GetName(), policyContext.InstanceLabel)
	if err != nil {
		return resp, errors.Wrap(err, "failed to procure tenant runtime")
	}

	policyList, err := policyRuntime.GetPolicyList(
		ctx,
		policyContext.GetId(),
		policyRuntime.PathFilter(TranslatePathSeparator(req.Options.PathSeparator), policyContext.Path),
	)
	if err != nil {
		return resp, errors.Wrap(err, "get policy list")
	}

	decisionFilter := initDecisionFilter(policyContext.Decisions)

	results := make(map[string]interface{})

	for _, policy := range policyList {
		queryStmt := "x = data." + policy.PackageName

		policyContext.Path = policy.PackageName
		input[InputPolicy] = policyContext

		qry, err := rego.New(
			rego.Compiler(policyRuntime.GetPluginsManager().GetCompiler()),
			rego.Store(policyRuntime.GetPluginsManager().Store),
			rego.Input(input),
			rego.Query(queryStmt),
		).PrepareForEval(ctx)

		if err != nil {
			return resp, cerr.ErrBadQuery.Err(err).Str("query", queryStmt)
		}

		packageName := policy.Package(TranslatePathSeparator(req.Options.PathSeparator))

		queryResults, err := qry.Eval(ctx, rego.EvalInput(input))

		if err != nil {
			return resp, cerr.ErrBadQuery.Err(err).Str("query", queryStmt).Msg("query evaluation failed")
		} else if len(queryResults) == 0 {
			return resp, cerr.ErrBadQuery.Err(err).Str("query", queryStmt).Msg("undefined results")
		}

		if result, ok := queryResults[0].Bindings["x"].(map[string]interface{}); ok {
			for k, v := range result {
				if !decisionFilter(k) {
					continue
				}
				if _, ok := v.(bool); !ok {
					continue
				}
				if results[packageName] == nil {
					results[packageName] = make(map[string]interface{})
				}
				if r, ok := results[packageName].(map[string]interface{}); ok {
					r[k] = v
				}
			}
		}
	}

	paths, err := structpb.NewStruct(results)
	if err != nil {
		return resp, err
	}

	resp = &authz2.DecisionTreeResponse{
		PathRoot: policyContext.Path,
		Path:     paths,
	}

	return resp, nil
}

// Is decision eval function.
func (s *AuthorizerServer) Is(ctx context.Context, req *authz2.IsRequest) (*authz2.IsResponse, error) { // nolint:funlen,gocyclo //TODO: split into smaller functions after merge with onebox
	log := grpcutil.CompleteLogger(ctx, s.logger)

	resp := &authz2.IsResponse{
		Decisions: make([]*authz2.Decision, 0),
	}

	policyContext := getPolicyInfoFromContext(ctx)

	if policyContext.Path == "" {
		return resp, cerr.ErrInvalidArgument.Msg("policy context path not set in header aserto-policy-path")
	}

	if len(policyContext.Decisions) == 0 {
		return resp, cerr.ErrInvalidArgument.Msg("policy context decisions not set")
	}

	if req.ResourceContext == nil {
		req.ResourceContext = pb.NewStruct()
	}

	if req.IdentityContext == nil {
		return resp, cerr.ErrInvalidArgument.Msg("identity context not set")
	}

	if req.IdentityContext.Type == api.IdentityType_IDENTITY_TYPE_UNKNOWN {
		return resp, cerr.ErrInvalidArgument.Msg("identity type UNKNOWN")
	}

	user, err := s.getUserFromIdentityContext(ctx, req.IdentityContext)
	if err != nil {
		log.Error().Err(err).Interface("req", req).Msg("failed to resolve identity context")
		return resp, cerr.ErrUserNotFound.WithGRPCStatus(codes.NotFound).Msg("failed to resolve identity context")
	}

	input := map[string]interface{}{
		InputUser:     convert(user),
		InputIdentity: convert(req.IdentityContext),
		InputPolicy:   policyContext,
		InputResource: req.ResourceContext,
	}

	queryStmt := fmt.Sprintf("x = data.%s", policyContext.Path)

	log.Debug().Interface("input", input).Msg("calculating is")

	policyRuntime, err := s.runtimeResolver.RuntimeFromContext(ctx, policyContext.GetId(), policyContext.GetName(), policyContext.InstanceLabel)
	if err != nil {
		return resp, errors.Wrap(err, "failed to procure tenant runtime")
	}

	query, err := rego.New(
		rego.Compiler(policyRuntime.GetPluginsManager().GetCompiler()),
		rego.Store(policyRuntime.GetPluginsManager().Store),
		rego.Query(queryStmt),
	).PrepareForEval(ctx)

	if err != nil {
		return resp, cerr.ErrBadQuery.Err(err).Str("query", queryStmt)
	}

	results, err := query.Eval(ctx, rego.EvalInput(input))

	if err != nil {
		return resp, cerr.ErrBadQuery.Err(err).Str("query", queryStmt).Msg("query evaluation failed")
	} else if len(results) == 0 {
		return resp, cerr.ErrBadQuery.Err(err).Str("query", queryStmt).Msg("undefined results")
	}

	v := results[0].Bindings["x"]
	outcomes := map[string]bool{}

	for _, d := range policyContext.Decisions {
		decision := authz2.Decision{
			Decision: d,
		}
		decision.Is, err = is(v, d)
		if err != nil {
			return nil, errors.Wrapf(err, "failed getting outcome for decision [%s]", d)
		}
		resp.Decisions = append(resp.Decisions, &decision)
		outcomes[decision.Decision] = decision.Is
	}

	dplugin := decisionlog_plugin.Lookup(policyRuntime.GetPluginsManager())
	d := api_v2.Decision{
		Id:        uuid.NewString(),
		Timestamp: timestamppb.New(time.Now().In(time.UTC)),
		Path:      policyContext.Path,
		Policy: &api_v2.DecisionPolicy{
			Context: policyContext,
		},
		User: &api_v2.DecisionUser{
			Context: req.IdentityContext,
			Id:      getID(input),
			Email:   getEmail(input),
		},
		Resource: req.ResourceContext,
		Outcomes: outcomes,
	}

	if dplugin == nil {
		return resp, err
	}

	err = dplugin.Log(ctx, &d)
	if err != nil {
		return resp, err
	}

	return resp, err
}

func is(v interface{}, decision string) (bool, error) {
	switch x := v.(type) {
	case bool:
		outcome := v.(bool)
		return outcome, nil
	case map[string]interface{}:
		m := v.(map[string]interface{})
		if _, ok := m[decision]; !ok {
			return false, cerr.ErrInvalidDecision.Msgf("decision element [%s] not found", decision)
		}
		outcome, err := is(m[decision], decision)
		if err != nil {
			return false, cerr.ErrInvalidDecision.Err(err)
		}
		return outcome, nil
	default:
		return false, cerr.ErrInvalidDecision.Msgf("is unexpected type %T", x)
	}
}

func (s *AuthorizerServer) Query(ctx context.Context, req *authz2.QueryRequest) (*authz2.QueryResponse, error) { // nolint:funlen,gocyclo //TODO: split into smaller functions after merge with onebox
	log := grpcutil.CompleteLogger(ctx, s.logger)

	if req.Query == "" {
		return &authz2.QueryResponse{}, cerr.ErrInvalidArgument.Msg("query not set")
	}

	if req.Options == nil {
		req.Options = &authz2.QueryOptions{
			Metrics:      false,
			Instrument:   false,
			Trace:        authz2.TraceLevel_TRACE_LEVEL_OFF,
			TraceSummary: false,
		}
	}

	if req.Options.Trace == authz2.TraceLevel_TRACE_LEVEL_UNKNOWN {
		req.Options.Trace = authz2.TraceLevel_TRACE_LEVEL_OFF
	}

	var input map[string]interface{}

	if req.Input != "" {
		if err := json.Unmarshal([]byte(req.Input), &input); err != nil {
			return &authz2.QueryResponse{}, errors.Wrap(err, "failed to unmarshal input - make sure it's a valid JSON object")
		}
	}

	if input == nil {
		input = make(map[string]interface{})
	}
	policyContext := getPolicyInfoFromContext(ctx)
	if policyContext != nil {
		input[InputPolicy] = policyContext
	}

	if s.cfg.API.EnableResourceContext {
		if req.ResourceContext != nil {
			input[InputResource] = req.ResourceContext
		}
	}

	if s.cfg.API.EnableIdentityContext {
		if req.IdentityContext != nil {
			if req.IdentityContext.Type == api.IdentityType_IDENTITY_TYPE_UNKNOWN {
				return &authz2.QueryResponse{}, cerr.ErrInvalidArgument.Msg("identity type UNKNOWN")
			}

			if req.IdentityContext.Type != api.IdentityType_IDENTITY_TYPE_NONE {
				input[InputIdentity] = convert(req.IdentityContext)
			}
		}

		if req.IdentityContext != nil && req.IdentityContext.Type != api.IdentityType_IDENTITY_TYPE_NONE {
			user, err := s.getUserFromIdentityContext(ctx, req.IdentityContext)
			if err != nil || user == nil {
				if err != nil {
					log.Error().Err(err).Interface("req", req).Msg("failed to resolve identity context")
				}

				return &authz2.QueryResponse{}, cerr.ErrAuthenticationFailed.WithGRPCStatus(codes.NotFound).Msg("failed to resolve identity context")
			}

			input[InputUser] = convert(user)
		}
	}

	log.Debug().Str("query", req.Query).Interface("input", input).Msg("executing query")
	var rt *runtime.Runtime
	var err error
	if policyContext != nil {
		rt, err = s.runtimeResolver.RuntimeFromContext(ctx, policyContext.GetId(), policyContext.GetName(), policyContext.InstanceLabel)
		if err != nil {
			return &authz2.QueryResponse{}, errors.Wrap(err, "failed to procure tenant runtime")
		}
	} else {
		rt, err = s.runtimeResolver.RuntimeFromContext(ctx, "", "", "")
		if err != nil {
			return &authz2.QueryResponse{}, cerr.ErrInvalidPolicyID.Msg("undefined policy context")
		}
	}

	queryResult, err := rt.Query(
		ctx,
		req.Query,
		input,
		req.Options.TraceSummary,
		req.Options.Metrics,
		req.Options.Instrument,
		TraceLevelToExplainModeV2(req.Options.Trace),
	)
	if err != nil {
		return &authz2.QueryResponse{}, err
	}

	resp := &authz2.QueryResponse{}
	queryResultJson, err := json.Marshal(queryResult.Result)
	if err != nil {
		return resp, err
	}

	var queryResultMap []interface{}
	err = json.Unmarshal(queryResultJson, &queryResultMap)
	if err != nil {
		return resp, err
	}
	respMap := make(map[string]interface{})
	respMap["result"] = queryResultMap
	resp.Response, err = protoutil.NewStruct(respMap)
	if err != nil {
		return resp, err
	}

	// metrics
	if queryResult.Metrics != nil {
		if metricsStruct, errX := structpb.NewStruct(queryResult.Metrics); errX == nil {
			resp.Metrics = metricsStruct
		}
	} else {
		resp.Metrics, _ = structpb.NewStruct(make(map[string]interface{}))
	}

	// trace (explanation)
	if queryResult.Explanation != nil {
		var v []interface{}
		if err = json.Unmarshal(queryResult.Explanation, &v); err != nil {
			return resp, errors.Wrap(err, "unmarshal json")
		}

		list, err := structpb.NewList(v)
		if err != nil {
			rt.Logger.Error().Err(err).Msg("newList")
		}

		if req.Options.TraceSummary {
			for _, val := range list.Values {
				resp.TraceSummary = append(resp.TraceSummary, val.GetStringValue())
			}
		} else {
			for _, val := range list.Values {
				resp.Trace = append(resp.Trace, val.GetStructValue())
			}
		}
	}

	return resp, nil
}

func (s *AuthorizerServer) Compile(ctx context.Context, req *authz2.CompileRequest) (*authz2.CompileResponse, error) { // nolint:funlen,gocyclo //TODO: split into smaller functions after merge with onebox
	log := grpcutil.CompleteLogger(ctx, s.logger)

	if req.Query == "" {
		return &authz2.CompileResponse{}, cerr.ErrInvalidArgument.Msg("query not set")
	}

	if req.Options == nil {
		req.Options = &authz2.QueryOptions{
			Metrics:      false,
			Instrument:   false,
			Trace:        authz2.TraceLevel_TRACE_LEVEL_OFF,
			TraceSummary: false,
		}
	}

	if req.Options.Trace == authz2.TraceLevel_TRACE_LEVEL_UNKNOWN {
		req.Options.Trace = authz2.TraceLevel_TRACE_LEVEL_OFF
	}

	var input map[string]interface{}

	if req.Input != "" {
		if err := json.Unmarshal([]byte(req.Input), &input); err != nil {
			return &authz2.CompileResponse{}, errors.Wrap(err, "failed to unmarshal input - make sure it's a valid JSON object")
		}
	}

	if s.cfg.API.EnableResourceContext {
		if req.ResourceContext != nil {
			input[InputResource] = req.ResourceContext
		}
	}

	if s.cfg.API.EnableIdentityContext {
		if req.IdentityContext != nil {
			if req.IdentityContext.Type == api.IdentityType_IDENTITY_TYPE_UNKNOWN {
				return &authz2.CompileResponse{}, cerr.ErrInvalidArgument.Msg("identity type UNKNOWN")
			}

			if req.IdentityContext.Type != api.IdentityType_IDENTITY_TYPE_NONE {
				input[InputIdentity] = convert(req.IdentityContext)
			}
		}

		if req.IdentityContext != nil && req.IdentityContext.Type != api.IdentityType_IDENTITY_TYPE_NONE {
			user, err := s.getUserFromIdentityContext(ctx, req.IdentityContext)
			if err != nil || user == nil {
				if err != nil {
					log.Error().Err(err).Interface("req", req).Msg("failed to resolve identity context")
				}

				return &authz2.CompileResponse{}, cerr.ErrAuthenticationFailed.WithGRPCStatus(codes.NotFound).Msg("failed to resolve identity context")
			}

			input[InputUser] = convert(user)
		}
	}

	policyContext := getPolicyInfoFromContext(ctx)
	if policyContext != nil {
		input[InputPolicy] = policyContext
	}

	if input == nil {
		input = make(map[string]interface{})
	}
	log.Debug().Str("query", req.Query).Interface("input", input).Msg("executing query")
	var rt *runtime.Runtime
	var err error
	if policyContext != nil {
		rt, err = s.runtimeResolver.RuntimeFromContext(ctx, policyContext.GetId(), policyContext.GetName(), policyContext.InstanceLabel)
		if err != nil {
			return &authz2.CompileResponse{}, errors.Wrap(err, "failed to procure tenant runtime")
		}
	} else {
		rt, err = s.runtimeResolver.RuntimeFromContext(ctx, "", "", "")
		if err != nil {
			return &authz2.CompileResponse{}, cerr.ErrInvalidPolicyID.Msg("undefined policy context")
		}
	}

	compileResult, err := rt.Compile(ctx, req.Query,
		input,
		req.Unknowns,
		req.DisableInlining,
		true,
		req.Options.Metrics,
		req.Options.Instrument,
		TraceLevelToExplainModeV2(req.Options.Trace))
	resp := &authz2.CompileResponse{}
	if err != nil {
		return resp, err
	}
	compileResultJson, err := json.Marshal(compileResult.Result)
	if err != nil {
		return resp, err
	}

	var compileResultMap map[string]interface{}
	err = json.Unmarshal(compileResultJson, &compileResultMap)
	if err != nil {
		return resp, err
	}
	resp.Response, err = protoutil.NewStruct(compileResultMap)
	if err != nil {
		return resp, err
	}
	// metrics
	if compileResult.Metrics != nil {
		if metricsStruct, errX := protoutil.NewStruct(compileResult.Metrics); errX == nil {
			resp.Metrics = metricsStruct
		}
	} else {
		resp.Metrics, _ = protoutil.NewStruct(make(map[string]interface{}))
	}

	// trace (explanation)
	if compileResult.Explanation != nil {
		var v []interface{}
		if err = json.Unmarshal(compileResult.Explanation, &v); err != nil {
			return resp, errors.Wrap(err, "unmarshal json")
		}

		list, err := protoutil.NewList(v)
		if err != nil {
			rt.Logger.Error().Err(err).Msg("newList")
		}

		if req.Options.TraceSummary {
			for _, val := range list.Values {
				resp.TraceSummary = append(resp.TraceSummary, val.GetStringValue())
			}
		} else {
			for _, val := range list.Values {
				resp.Trace = append(resp.Trace, val.GetStructValue())
			}
		}
	}
	return resp, nil
}

// convert, explicitly convert from proto message interface{} in order
// to preserve enum values as strings when marshaled to JSON
func convert(msg proto.Message) interface{} {
	b, err := protojson.MarshalOptions{
		Multiline:       false,
		Indent:          "  ",
		AllowPartial:    false,
		UseProtoNames:   true,
		UseEnumNumbers:  false,
		EmitUnpopulated: true,
	}.Marshal(msg)
	if err != nil {
		return nil
	}

	var v interface{}
	if err := json.Unmarshal(b, &v); err != nil {
		return nil
	}

	return v
}

func TraceLevelToExplainModeV2(t authz2.TraceLevel) types.ExplainModeV1 {
	switch t {
	case authz2.TraceLevel_TRACE_LEVEL_UNKNOWN:
		return types.ExplainOffV1
	case authz2.TraceLevel_TRACE_LEVEL_OFF:
		return types.ExplainOffV1
	case authz2.TraceLevel_TRACE_LEVEL_FULL:
		return types.ExplainFullV1
	case authz2.TraceLevel_TRACE_LEVEL_NOTES:
		return types.ExplainNotesV1
	case authz2.TraceLevel_TRACE_LEVEL_FAILS:
		return types.ExplainFailsV1
	default:
		return types.ExplainOffV1
	}
}

// TranslatePathSeparator is a helper function that transforms from the v2 format to v1 format
// Should be removed when deprecating v1
func TranslatePathSeparator(separator authz2.PathSeparator) authz.PathSeparator {
	switch separator {
	case authz2.PathSeparator_PATH_SEPARATOR_SLASH:
		return authz.PathSeparator_PATH_SEPARATOR_SLASH
	case authz2.PathSeparator_PATH_SEPARATOR_DOT:
		return authz.PathSeparator_PATH_SEPARATOR_DOT
	default:
		return authz.PathSeparator_PATH_SEPARATOR_UNKNOWN
	}
}

func getPolicyInfoFromContext(ctx context.Context) *api.PolicyContext {
	result := api.PolicyContext{}

	id := ctx.Value("aserto-policy-id")
	if id != nil {
		result.Id = fmt.Sprintf("%s", id)
	}
	name := ctx.Value("aserto-policy-name")
	if name != nil {
		result.Name = fmt.Sprintf("%s", name)
	}
	path := ctx.Value("aserto-policy-path")
	if path != nil {
		result.Path = fmt.Sprintf("%s", path)
	}
	decisions := ctx.Value("aserto-policy-decisions")
	if decisions != nil {
		result.Decisions = strings.Split(fmt.Sprintf("%s", path), ",")
	}
	label := ctx.Value("aserto-instance-label")
	if label != nil {
		result.InstanceLabel = fmt.Sprintf("%s", label)
	}
	if result.Id == "" || (result.Name == "" && result.InstanceLabel == "") {
		return nil
	}
	return &result
}

func initDecisionFilter(decisions []string) func(decision string) bool {
	if len(decisions) == 1 && decisions[0] == "*" {
		return func(s string) bool {
			return true
		}
	}

	decisionMap := make(map[string]struct{})
	for _, v := range decisions {
		decisionMap[v] = struct{}{}
	}

	return func(s string) bool {
		_, ok := decisionMap[s]
		return ok
	}
}

func getID(v map[string]interface{}) string {
	if u, ok := v["user"].(map[string]interface{}); ok {
		if i, ok := u["id"].(string); ok {
			return i
		}
	}
	return ""
}

func getEmail(v map[string]interface{}) string {
	if u, ok := v["user"].(map[string]interface{}); ok {
		if e, ok := u["email"].(string); ok {
			return e
		}
		if p, ok := u["properties"].(map[string]interface{}); ok {
			if e, ok := p["email"].(string); ok {
				return e
			}
		}
	}
	return ""
}
