package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/types"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type healthWorkerTransport func(*http.Request) (*http.Response, error)

func (f healthWorkerTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func workerResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func healthWorkerFixture(t *testing.T) (*ZTAPIHealthWorker, *time.Time, *int) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "worker.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(model.ZTAPIProbeMigrationTypes()...))
	_, err = model.InitializeZTAPIProbeAllocation(context.Background(), db, "parent-r7-transfer", 100_000_000)
	require.NoError(t, err)
	now := time.Unix(2_000_000_000, 0)
	sends := 0
	config := DefaultZTAPIHealthWorkerConfig()
	config.ProbeKey = "offline-dedicated-key"
	config.ProbeUserID, config.ProbeIdentityValidated = 999, true
	config.RelayBaseURL = "http://127.0.0.1:3000"
	config.Now = func() time.Time { return now }
	config.ProbeTransport = healthWorkerTransport(func(r *http.Request) (*http.Response, error) {
		sends++
		require.Equal(t, "Bearer offline-dedicated-key", r.Header.Get("Authorization"))
		require.Empty(t, r.Header.Get("X-ZTAPI-Health-Source"))
		return workerResponse(200, `{"choices":[{"message":{"content":"77"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":1}}`), nil
	})
	target := model.ZTAPIProbeTarget{ModelID: 1, SourceModel: "source", PublicModel: "public", Protocol: "chat", Generation: 1, ConfigVersion: 1, InputNanoUSDPerMillion: 1_000_000_000, OutputNanoUSDPerMillion: 2_000_000_000}
	backend := ZTAPIHealthWorkerBackend{
		Probes: &model.ZTAPIProbeStore{DB: db},
		ListTargets: func(context.Context, int) ([]model.ZTAPIProbeTarget, error) {
			other := target
			other.Stream = true
			return []model.ZTAPIProbeTarget{target, other}, nil
		},
		CheckProbe: func(_ context.Context, _ *gorm.DB, target model.ZTAPIProbeTarget, _ time.Time) (model.ZTAPIProbeAdmission, error) {
			return model.ZTAPIProbeAdmission{Target: target, Active: true}, nil
		},
		ProbeFinalized: func(context.Context, model.ZTAPIProbeJob, string) (bool, error) { return true, nil },
	}
	w, err := NewZTAPIHealthWorker(backend, config)
	require.NoError(t, err)
	return w, &now, &sends
}

func TestZTAPIHealthWorkerOneModeCoverageAndNoDuplicateEvents(t *testing.T) {
	w, now, sends := healthWorkerFixture(t)
	checks := 0
	w.backend.CheckProbe = func(_ context.Context, _ *gorm.DB, target model.ZTAPIProbeTarget, _ time.Time) (model.ZTAPIProbeAdmission, error) {
		checks++
		return model.ZTAPIProbeAdmission{Target: target, Active: true, RealCoverage: target.Stream}, nil
	}
	finalized := 0
	w.backend.ProbeFinalized = func(context.Context, model.ZTAPIProbeJob, string) (bool, error) { finalized++; return true, nil }
	require.NoError(t, w.RunOnce(context.Background()))
	require.Equal(t, 1, *sends)
	require.Equal(t, 2, checks)
	require.Equal(t, 1, finalized)
	require.NoError(t, w.RunOnce(context.Background()))
	require.Equal(t, 1, *sends)
	*now = now.Add(time.Minute)
	require.NoError(t, w.RunOnce(context.Background()))
	require.Equal(t, 1, *sends)
	// There is deliberately no RecordOutcome callback on the worker backend.
}

func TestZTAPIHealthWorkerMissingIdentityAndFinalResult(t *testing.T) {
	w, _, sends := healthWorkerFixture(t)
	w.config.ProbeIdentityValidated = false
	codes := []string{}
	w.backend.OperationalStatus = func(_ context.Context, code, _ string) { codes = append(codes, code) }
	require.NoError(t, w.RunOnce(context.Background()))
	require.Zero(t, *sends)
	require.Contains(t, codes, "probe_identity_missing")
	w.config.ProbeIdentityValidated = true
	w.backend.ProbeFinalized = nil
	require.NoError(t, w.RunOnce(context.Background()))
	var jobs []model.ZTAPIProbeJob
	require.NoError(t, w.backend.Probes.DB.Find(&jobs).Error)
	for _, job := range jobs {
		require.Equal(t, "unknown", job.State)
	}
	require.Contains(t, codes, "probe_final_result_missing")
}

func TestZTAPIHealthWorkerHTTPProtocolsAndTruncation(t *testing.T) {
	cases := []struct {
		name, protocol, body, code string
		stream                     bool
		input                      int64
	}{
		{"chat", "chat", `{"choices":[{"message":{"content":"77"},"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"completion_tokens":1}}`, "functional_pass", false, 9},
		{"wrong", "chat", `{"choices":[{"message":{"content":"78"},"finish_reason":"stop"}]}`, "wrong_answer", false, 0},
		{"length", "chat", `{"choices":[{"message":{"content":"77"},"finish_reason":"length"}]}`, "incomplete_response", false, 0},
		{"chat-stream", "chat", "data: {\"choices\":[{\"delta\":{\"content\":\"77\"},\"finish_reason\":\"stop\"}]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":8,\"completion_tokens\":1}}\n\ndata: [DONE]\n\n", "functional_pass", true, 8},
		{"chat-truncated", "chat", "data: {\"choices\":[{\"delta\":{\"content\":\"77\"},\"finish_reason\":\"stop\"}]}\n\n", "incomplete_response", true, 0},
		{"responses", "responses", `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"77"}]}],"usage":{"input_tokens":7,"output_tokens":1}}`, "functional_pass", false, 7},
		{"responses-stream", "responses", "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"77\"}]}],\"usage\":{\"input_tokens\":6,\"output_tokens\":1}}}\n\n", "functional_pass", true, 6},
		{"responses-truncated", "responses", "data: {\"type\":\"response.output_text.delta\",\"delta\":\"77\"}\n\n", "incomplete_response", true, 0},
		{"invalid", "chat", `{"choices":`, "invalid_response", false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			config := DefaultZTAPIHealthWorkerConfig()
			config.ProbeKey, config.RelayBaseURL = "offline", "http://127.0.0.1:3000"
			config.ProbeTransport = healthWorkerTransport(func(r *http.Request) (*http.Response, error) {
				var body map[string]any
				require.NoError(t, common.DecodeJson(r.Body, &body))
				require.Equal(t, tc.stream, body["stream"])
				if tc.protocol == "responses" {
					require.Equal(t, "You are a concise assistant.", body["instructions"])
					require.Equal(t, "/v1/responses", r.URL.Path)
					require.EqualValues(t, 1024, body["max_output_tokens"])
					require.Contains(t, body["input"], "35+42")
				} else {
					messages := body["messages"].([]any)
					require.Equal(t, map[string]any{"role": "system", "content": "You are a concise assistant."}, messages[0])
					require.Equal(t, "/v1/chat/completions", r.URL.Path)
					require.EqualValues(t, 1024, body["max_tokens"])
				}
				return workerResponse(200, tc.body), nil
			})
			result := performZTAPIHealthProbe(context.Background(), config, model.ZTAPIProbeJob{ID: "offline-job", ZTAPIProbeTarget: model.ZTAPIProbeTarget{PublicModel: "public", Protocol: tc.protocol, Stream: tc.stream}})
			require.Equal(t, tc.code, result.Code)
			require.Equal(t, tc.input, result.InputTokens)
		})
	}
}

func TestZTAPIHealthWorkerMediaProtocolsUseFrozenMinimumOptions(t *testing.T) {
	for _, tc := range []struct {
		protocol, modality, operation, payload, path, response string
	}{
		{"images", model.ZTAPIModalityImage, "image_generate", `{"n":1,"quality":"standard","response_format":"url","size":"1024x1024"}`, "/v1/images/generations", `{"data":[{"url":"https://cdn.example.test/probe.png"}]}`},
		{"video-tasks", model.ZTAPIModalityVideo, "video_submit", `{"duration":5,"size":"720p"}`, "/v1/videos", `{"id":"provider-task-probe"}`},
	} {
		t.Run(tc.protocol, func(t *testing.T) {
			config := DefaultZTAPIHealthWorkerConfig()
			config.ProbeKey, config.RelayBaseURL = "offline", "http://127.0.0.1:3000"
			config.ProbeTransport = healthWorkerTransport(func(r *http.Request) (*http.Response, error) {
				require.Equal(t, tc.path, r.URL.Path)
				var payload map[string]any
				require.NoError(t, common.DecodeJson(r.Body, &payload))
				require.Equal(t, "public-media", payload["model"])
				require.NotEmpty(t, payload["prompt"])
				if tc.protocol == "images" {
					require.Equal(t, "1024x1024", payload["size"])
					require.EqualValues(t, 1, payload["n"])
				} else {
					require.Equal(t, "720p", payload["size"])
					require.EqualValues(t, 5, payload["duration"])
				}
				return workerResponse(http.StatusOK, tc.response), nil
			})
			job := model.ZTAPIProbeJob{ID: "media-job", ZTAPIProbeTarget: model.ZTAPIProbeTarget{
				ModelID: 1, PublicModel: "public-media", SourceModel: "provider-media", Protocol: tc.protocol,
				Modality: tc.modality, Operation: tc.operation, ProbePayloadJSON: tc.payload, FixedCostNanoUSD: 1,
			}}
			result := performZTAPIHealthProbe(context.Background(), config, job)
			require.True(t, result.Complete)
			require.Equal(t, "functional_pass", result.Code)
		})
	}
}

func TestZTAPIHealthWorkerMediaProbeRejectsEmptyResult(t *testing.T) {
	config := DefaultZTAPIHealthWorkerConfig()
	config.ProbeKey, config.RelayBaseURL = "offline", "http://127.0.0.1:3000"
	config.ProbeTransport = healthWorkerTransport(func(*http.Request) (*http.Response, error) {
		return workerResponse(http.StatusOK, `{"data":[]}`), nil
	})
	job := model.ZTAPIProbeJob{ID: "empty-media", ZTAPIProbeTarget: model.ZTAPIProbeTarget{
		PublicModel: "public-image", Protocol: "images", Modality: model.ZTAPIModalityImage,
		Operation: "image_generate", ProbePayloadJSON: `{"size":"512x512","quality":"standard","response_format":"url","n":1}`, FixedCostNanoUSD: 1,
	}}
	result := performZTAPIHealthProbe(context.Background(), config, job)
	require.False(t, result.Complete)
	require.Equal(t, "invalid_response", result.Code)
}

func TestZTAPIHealthWorkerMediaProbeRejectsUnsafeFrozenOptions(t *testing.T) {
	config := DefaultZTAPIHealthWorkerConfig()
	config.ProbeKey, config.RelayBaseURL = "offline", "http://127.0.0.1:3000"
	calls := 0
	config.ProbeTransport = healthWorkerTransport(func(*http.Request) (*http.Response, error) {
		calls++
		return workerResponse(http.StatusOK, `{"data":[{"url":"https://cdn.example.test/probe.png"}]}`), nil
	})
	cases := []model.ZTAPIProbeTarget{
		{Protocol: "images", Modality: model.ZTAPIModalityImage, Operation: types.ZTAPIHealthOperationImageGenerate, PublicModel: "image", FixedCostNanoUSD: 1, ProbePayloadJSON: `{"size":"512x512","quality":"standard","response_format":"url","n":"1"}`},
		{Protocol: "images", Modality: model.ZTAPIModalityImage, Operation: types.ZTAPIHealthOperationImageGenerate, PublicModel: "image", FixedCostNanoUSD: 1, ProbePayloadJSON: `{"size":"512x512","quality":"standard","response_format":"url","n":1,"user":"attacker"}`},
		{Protocol: "video-tasks", Modality: model.ZTAPIModalityVideo, Operation: types.ZTAPIHealthOperationVideoSubmit, PublicModel: "video", FixedCostNanoUSD: 1, ProbePayloadJSON: `{"size":"720p","duration":0}`},
		{Protocol: "video-tasks", Modality: model.ZTAPIModalityVideo, Operation: types.ZTAPIHealthOperationVideoSubmit, PublicModel: "video", FixedCostNanoUSD: 1, ProbePayloadJSON: `{"size":"720p"}`},
	}
	for i, target := range cases {
		job := model.ZTAPIProbeJob{ID: fmt.Sprintf("unsafe-media-%d", i), ZTAPIProbeTarget: target}
		result := performZTAPIHealthProbe(context.Background(), config, job)
		require.Equal(t, "probe_payload_error", result.Code)
		require.False(t, result.Complete)
	}
	require.Zero(t, calls, "invalid frozen options must fail before any HTTP dispatch")
}

func TestZTAPIHealthWorkerBuildsMinimumCostImageProbeFromFrozenEvidence(t *testing.T) {
	priceJSON, err := types.CanonicalizeZTAPIMediaPriceContract(`{
		"version":1,"modality":"image","rules":[
			{"id":"gt_200k","conditions":{"prompt_tokens_tier":"gt_200k"},"billing_unit":"usd_per_million_tokens","cost_usd":{"input_tokens":"2","output_tokens":"4"},"sale_usd":{"input_tokens":"3.3333333333","output_tokens":"6.6666666667"},"source_cells":{"input_tokens":"A2","output_tokens":"B2"}},
			{"id":"lte_200k","conditions":{"prompt_tokens_tier":"lte_200k"},"billing_unit":"usd_per_million_tokens","cost_usd":{"input_tokens":"1","output_tokens":"2"},"sale_usd":{"input_tokens":"1.6666666667","output_tokens":"3.3333333333"},"source_cells":{"input_tokens":"A1","output_tokens":"B1"}}
		]}`)
	require.NoError(t, err)
	_, protocolJSON, err := types.SealZTAPIImageProtocolContract(types.ZTAPIImageProtocolContract{
		Version: types.ZTAPIImageProtocolContractVersion, ProviderModel: "provider-image", EndpointType: types.ZTAPIImageEndpointGeneration,
		Method: "POST", Path: "/v1/images/generations",
		Capabilities: types.ZTAPIImageCapabilities{Sizes: []string{"1024x1024", "512x512"}, Qualities: []string{"standard"}, ResponseFormats: []string{"url"}, MinCount: 1, MaxCount: 1},
		Response:     types.ZTAPIImageResponseContract{Schema: "object_results_array", ResultsField: "data", ResultFields: map[string]string{"url": "url"}},
		Usage:        types.ZTAPIImageUsageContract{UsageField: "usage", Fields: map[string]string{"input_tokens": "input_tokens", "output_tokens": "output_tokens"}, TotalField: "total_tokens", TotalSemantics: "sum_of_dimensions", CacheSemantics: "not_reported"},
		Reservations: []types.ZTAPIImageReservationAuthority{
			{Size: "1024x1024", Quality: "standard", ResponseFormat: "url", N: 1, MaximumDimensions: map[string]string{"input_tokens": "1000", "output_tokens": "2000"}},
			{Size: "512x512", Quality: "standard", ResponseFormat: "url", N: 1, MaximumDimensions: map[string]string{"input_tokens": "500", "output_tokens": "1000"}},
		}, RequestIDField: "request_id", EvidenceVersion: types.ZTAPIImageEvidenceVersion,
	})
	require.NoError(t, err)
	target, ok := ztapiMediaProbeTargetFromEvidence(model.ZTAPIModelConfig{ID: 5, SourceModel: "provider-image", Version: 3}, model.ZTAPIModelPublicationSnapshot{
		ID: 11, PublicName: "zt-image", PriceSourceID: 12, MediaPriceContractJSON: priceJSON, ImageProtocolContractJSON: protocolJSON,
	}, model.ZTAPIModelPriceSource{ID: 12, Version: 4, MediaPriceContractJSON: priceJSON}, model.ZTAPIHealthState{Generation: 2}, false)
	require.True(t, ok)
	require.Equal(t, "images", target.Protocol)
	require.Equal(t, "image_generate", target.Operation)
	require.EqualValues(t, 2_500_000, target.FixedCostNanoUSD)
	require.JSONEq(t, `{"n":1,"quality":"standard","response_format":"url","size":"512x512"}`, target.ProbePayloadJSON)
}

func TestZTAPIHealthWorkerGPTImageProbePricesOnlyReportedDimensions(t *testing.T) {
	price, _, protocol, _ := gptImageThreeDimensionContracts(t)
	candidate, err := cheapestZTAPIImageProbe(price, protocol)
	require.NoError(t, err)
	require.EqualValues(t, 516000, candidate.cost)
	require.JSONEq(t, `{"n":1,"quality":"low","response_format":"b64_json","size":"1024x1024"}`, candidate.payload)
}

func TestZTAPIHealthWorkerBuildsMinimumCostVideoProbeWithoutVideoInput(t *testing.T) {
	priceJSON, err := types.CanonicalizeZTAPIMediaPriceContract(`{
		"version":1,"modality":"video","rules":[
			{"id":"without_video_input","conditions":{"contains_video_input":"false"},"billing_unit":"usd_per_million_tokens","cost_usd":{"input_tokens":"1"},"sale_usd":{"input_tokens":"1.6666666667"},"source_cells":{"input_tokens":"A1"}},
			{"id":"with_video_input","conditions":{"contains_video_input":"true"},"billing_unit":"usd_per_million_tokens","cost_usd":{"input_tokens":"2"},"sale_usd":{"input_tokens":"3.3333333333"},"source_cells":{"input_tokens":"A2"}}
		]}`)
	require.NoError(t, err)
	_, protocolJSON, err := types.SealZTAPIVideoProtocolContract(types.ZTAPIVideoProtocolContract{
		Version: types.ZTAPIVideoProtocolContractVersion, Provider: "aihub", ProviderModel: "provider-video",
		Auth:         types.ZTAPIVideoAuthContract{Method: "header", Header: "Authorization", Scheme: "Bearer"},
		Create:       types.ZTAPIVideoEndpointContract{Method: "POST", Path: "/hub/v1/video/tasks", TaskIDField: "data.task_id", RequestIDField: "request_id"},
		Fetch:        types.ZTAPIVideoEndpointContract{Method: "GET", Path: "/hub/v1/video/tasks/{task_id}", TaskIDField: "data.task_id", RequestIDField: "request_id"},
		Callback:     types.ZTAPIVideoCallbackContract{Enabled: false},
		Request:      types.ZTAPIVideoRequestContract{ModelField: "model", PromptField: "prompt", ResolutionField: "resolution", DurationField: "duration"},
		Capabilities: types.ZTAPIVideoCapabilities{Resolutions: []string{"720p", "480p"}, DurationSeconds: []int{10, 5}, SupportsVideoInput: false},
		States:       types.ZTAPIVideoStateContract{Field: "data.status", Accepted: []string{"queued"}, Processing: []string{"processing"}, Succeeded: []string{"completed"}, Failed: []string{"failed"}},
		Result:       types.ZTAPIVideoResultContract{URLField: "data.result.url", ResolutionField: "data.result.resolution", DurationField: "data.result.duration", FailureReasonField: "data.error.message"},
		Usage:        types.ZTAPIVideoUsageContract{Fields: map[string]string{"input_tokens": "data.usage.input_tokens"}},
		Reservations: []types.ZTAPIVideoReservationAuthority{
			{Resolution: "720p", DurationSeconds: 10, MaximumDimensions: map[string]string{"input_tokens": "4000"}},
			{Resolution: "720p", DurationSeconds: 5, MaximumDimensions: map[string]string{"input_tokens": "2000"}},
			{Resolution: "480p", DurationSeconds: 10, MaximumDimensions: map[string]string{"input_tokens": "2500"}},
			{Resolution: "480p", DurationSeconds: 5, MaximumDimensions: map[string]string{"input_tokens": "1000"}},
		},
		EvidenceVersion: types.ZTAPIVideoEvidenceVersion,
	})
	require.NoError(t, err)
	target, ok := ztapiMediaProbeTargetFromEvidence(model.ZTAPIModelConfig{ID: 6, SourceModel: "provider-video", Version: 3}, model.ZTAPIModelPublicationSnapshot{
		ID: 13, PublicName: "zt-video", PriceSourceID: 14, MediaPriceContractJSON: priceJSON, VideoProtocolContractJSON: protocolJSON,
	}, model.ZTAPIModelPriceSource{ID: 14, Version: 5, MediaPriceContractJSON: priceJSON}, model.ZTAPIHealthState{Generation: 4}, false)
	require.True(t, ok)
	require.Equal(t, "video-tasks", target.Protocol)
	require.Equal(t, "video_submit", target.Operation)
	require.EqualValues(t, 1_000_000, target.FixedCostNanoUSD)
	require.JSONEq(t, `{"duration":5,"size":"480p"}`, target.ProbePayloadJSON)
}

func TestZTAPIHealthWorkerProductionMediaTargetStaysBlockedWhileQuotationMappingPending(t *testing.T) {
	w, _, _ := productionWorkerFixture(t)
	db := w.backend.Probes.DB
	priceJSON, err := types.CanonicalizeZTAPIMediaPriceContract(`{
		"version":1,"modality":"image","rules":[
			{"id":"gt_200k","conditions":{"prompt_tokens_tier":"gt_200k"},"billing_unit":"usd_per_million_tokens","cost_usd":{"input_tokens":"2","output_tokens":"4"},"sale_usd":{"input_tokens":"3.3333333333","output_tokens":"6.6666666667"},"source_cells":{"input_tokens":"A2","output_tokens":"B2"}},
			{"id":"lte_200k","conditions":{"prompt_tokens_tier":"lte_200k"},"billing_unit":"usd_per_million_tokens","cost_usd":{"input_tokens":"1","output_tokens":"2"},"sale_usd":{"input_tokens":"1.6666666667","output_tokens":"3.3333333333"},"source_cells":{"input_tokens":"A1","output_tokens":"B1"}}
		]}`)
	require.NoError(t, err)
	_, protocolJSON, err := types.SealZTAPIImageProtocolContract(types.ZTAPIImageProtocolContract{
		Version: types.ZTAPIImageProtocolContractVersion, ProviderModel: "gp-image-2", EndpointType: types.ZTAPIImageEndpointGeneration,
		Method: "POST", Path: "/v1/images/generations",
		Capabilities:   types.ZTAPIImageCapabilities{Sizes: []string{"512x512"}, Qualities: []string{"standard"}, ResponseFormats: []string{"url"}, MinCount: 1, MaxCount: 1},
		Response:       types.ZTAPIImageResponseContract{Schema: "object_results_array", ResultsField: "data", ResultFields: map[string]string{"url": "url"}},
		Usage:          types.ZTAPIImageUsageContract{UsageField: "usage", Fields: map[string]string{"input_tokens": "input_tokens", "output_tokens": "output_tokens"}, TotalField: "total_tokens", TotalSemantics: "sum_of_dimensions", CacheSemantics: "not_reported"},
		Reservations:   []types.ZTAPIImageReservationAuthority{{Size: "512x512", Quality: "standard", ResponseFormat: "url", N: 1, MaximumDimensions: map[string]string{"input_tokens": "500", "output_tokens": "1000"}}},
		RequestIDField: "request_id", EvidenceVersion: types.ZTAPIImageEvidenceVersion,
	})
	require.NoError(t, err)
	require.NoError(t, db.Model(&model.ZTAPIModelConfig{}).Where("id = ?", 1).Updates(map[string]any{
		"source_model": "gp-image-2", "input_cost_per_million": 0, "output_cost_per_million": 0,
	}).Error)
	require.NoError(t, db.Model(&model.ZTAPIModelPriceSource{}).Where("model_config_id = ?", 1).Updates(map[string]any{
		"source_model": "gp-image-2", "media_price_contract_json": priceJSON,
	}).Error)
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Model(&model.ZTAPIModelPublicationSnapshot{}).Where("model_config_id = ?", 1).Updates(map[string]any{
		"source_model": "gp-image-2", "media_price_contract_json": priceJSON, "image_protocol_contract_json": protocolJSON,
		"input_price_per_million": 0, "output_price_per_million": 0,
	}).Error)
	var config model.ZTAPIModelConfig
	var snapshot model.ZTAPIModelPublicationSnapshot
	var price model.ZTAPIModelPriceSource
	require.NoError(t, db.First(&config, 1).Error)
	require.NoError(t, db.First(&snapshot, config.PublicationSnapshotID).Error)
	require.NoError(t, db.First(&price, snapshot.PriceSourceID).Error)
	require.Equal(t, model.ZTAPIModalityText, model.ZTAPIModelModality(config.SourceModel), "the checked-in quotation still marks this row mapping_pending")
	_, directActive := ztapiMediaProbeTargetFromEvidence(config, snapshot, price, model.ZTAPIHealthState{Generation: 1}, false)
	require.True(t, directActive, "the frozen rows themselves must form a valid media target")
	_, active, err := productionZTAPIProbeTarget(context.Background(), db, 1, false, false)
	require.NoError(t, err)
	require.False(t, active, "pricing and protocol evidence must not bypass the quotation mapping gate")
	_, active, err = productionZTAPIProbeTarget(context.Background(), db, 1, true, false)
	require.NoError(t, err)
	require.False(t, active)
}

func TestZTAPIHealthWorkerMediaProbeDoesNotReadTokenSamples(t *testing.T) {
	samples, err := productionZTAPIProbeSamples(context.Background(), nil, nil, model.ZTAPIProbeTarget{
		ModelID: 1, Modality: model.ZTAPIModalityImage, Operation: types.ZTAPIHealthOperationImageGenerate, FixedCostNanoUSD: 1,
	}, time.Now())
	require.NoError(t, err)
	require.Empty(t, samples)
}

func TestZTAPIHealthWorkerAlertMissingRecipientAndRetry(t *testing.T) {
	w, now, _ := healthWorkerFixture(t)
	w.config.ProbeKey = ""
	claims, attempts, unpublishes := 0, 0, 0
	due := *now
	accepted := false
	w.backend.ClaimOutbox = func(_ context.Context, kind string, at time.Time, _ time.Duration, limit int) ([]ZTAPIHealthWorkItem, error) {
		require.Equal(t, 1, limit)
		if kind == "unpublish" {
			if unpublishes == 0 {
				return []ZTAPIHealthWorkItem{{ID: 2}}, nil
			}
			return nil, nil
		}
		claims++
		if accepted || at.Before(due) {
			return nil, nil
		}
		attempts++
		return []ZTAPIHealthWorkItem{{ID: 1, ModelID: 3, IncidentID: 4, Generation: 5, Attempts: attempts}}, nil
	}
	w.backend.Unpublish = func(context.Context, ZTAPIHealthWorkItem) error { unpublishes++; return nil }
	w.backend.FinishOutbox = func(_ context.Context, item ZTAPIHealthWorkItem, delivery ZTAPIHealthDelivery) error {
		if item.ID == 1 {
			accepted, due = delivery.Accepted, delivery.RetryAt
		}
		return nil
	}
	require.NoError(t, w.RunOnce(context.Background()))
	require.Zero(t, claims)
	require.False(t, accepted)
	require.Equal(t, 1, unpublishes)
	w.config.AlertWebhookURL = "https://alerts.invalid/health"
	sends := 0
	w.config.AlertTransport = healthWorkerTransport(func(r *http.Request) (*http.Response, error) {
		sends++
		payload, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.NotContains(t, string(payload), "offline-dedicated-key")
		require.NotContains(t, string(payload), "prompt")
		require.NotEmpty(t, r.Header.Get("Idempotency-Key"))
		if sends == 1 {
			return workerResponse(503, "private response must not be persisted"), nil
		}
		return workerResponse(204, ""), nil
	})
	require.NoError(t, w.RunOnce(context.Background()))
	require.False(t, accepted)
	require.Equal(t, now.Add(30*time.Second), due)
	require.NoError(t, w.RunOnce(context.Background()))
	require.Equal(t, 1, sends)
	*now = due
	require.NoError(t, w.RunOnce(context.Background()))
	require.True(t, accepted)
	require.Equal(t, 2, sends)
}

func TestZTAPIHealthWorkerBoundsRedirectAndCancel(t *testing.T) {
	config := DefaultZTAPIHealthWorkerConfig()
	config.ProbeKey, config.RelayBaseURL = "offline", "http://127.0.0.1:3000"
	job := model.ZTAPIProbeJob{ZTAPIProbeTarget: model.ZTAPIProbeTarget{Protocol: "chat", PublicModel: "public"}}
	for _, body := range []string{strings.Repeat("x", (1<<20)+1)} {
		config.ProbeTransport = healthWorkerTransport(func(*http.Request) (*http.Response, error) { return workerResponse(200, body), nil })
		require.Equal(t, "response_too_large", performZTAPIHealthProbe(context.Background(), config, job).Code)
	}
	calls := 0
	config.ProbeTransport = healthWorkerTransport(func(*http.Request) (*http.Response, error) {
		calls++
		r := workerResponse(302, "")
		r.Header.Set("Location", "https://upstream.invalid/steal")
		return r, nil
	})
	require.Equal(t, "http_status", performZTAPIHealthProbe(context.Background(), config, job).Code)
	require.Equal(t, 1, calls)
	config.RelayBaseURL = "https://upstream.invalid"
	require.Equal(t, "relay_url_invalid", performZTAPIHealthProbe(context.Background(), config, job).Code)
	require.Equal(t, 1, calls)
	w, _, _ := healthWorkerFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, w.RunOnce(ctx), context.Canceled)
	w.running.Store(true)
	require.ErrorIs(t, w.RunOnce(context.Background()), ErrZTAPIHealthWorkerBusy)
	w.running.Store(false)
	w.config.ProbeTransport = healthWorkerTransport(func(*http.Request) (*http.Response, error) { return nil, errors.New("secret-containing-error") })
	require.NoError(t, w.RunOnce(context.Background()))
}

func TestZTAPIHealthWorkerEngineAdapterDurableAlertRetry(t *testing.T) {
	w, now, _ := healthWorkerFixture(t)
	w.config.ProbeKey = ""
	require.NoError(t, model.MigrateZTAPIHealth(w.backend.Probes.DB))
	s := model.NewZTAPIHealthStore(w.backend.Probes.DB)
	s.Now = w.config.Now
	var err error
	w.backend, err = AttachZTAPIHealthStore(w.backend, s, 999)
	require.NoError(t, err)
	job := model.ZTAPIHealthOutbox{DedupKey: "incident:42:alert", Kind: "alert", ModelID: 1, IncidentID: 42, Generation: 1, Status: "pending", NextAttemptAt: now.Unix()}
	require.NoError(t, s.DB.Create(&job).Error)
	require.NoError(t, w.RunOnce(context.Background()))
	require.NoError(t, s.DB.First(&job, job.ID).Error)
	require.Equal(t, "pending", job.Status)
	require.Zero(t, job.Attempts)
	w.config.AlertWebhookURL = "https://alerts.invalid/health"
	w.config.AlertTransport = healthWorkerTransport(func(*http.Request) (*http.Response, error) { return workerResponse(503, "secret"), nil })
	require.NoError(t, w.RunOnce(context.Background()))
	require.NoError(t, s.DB.First(&job, job.ID).Error)
	require.Equal(t, "pending", job.Status)
	require.Equal(t, 1, job.Attempts)
	require.Equal(t, "alert_http_status", job.LastError)
	require.Equal(t, now.Add(30*time.Second).Unix(), job.NextAttemptAt)
	*now = now.Add(30 * time.Second)
	w.config.AlertTransport = healthWorkerTransport(func(*http.Request) (*http.Response, error) { return workerResponse(204, ""), nil })
	require.NoError(t, w.RunOnce(context.Background()))
	require.NoError(t, s.DB.First(&job, job.ID).Error)
	require.Equal(t, "done", job.Status)
	require.Equal(t, 2, job.Attempts)
	require.Equal(t, now.Unix(), job.DeliveredAt)
}

func TestZTAPIHealthWorkerEngineCoverageAndFinalization(t *testing.T) {
	w, now, _ := healthWorkerFixture(t)
	require.NoError(t, model.MigrateZTAPIHealth(w.backend.Probes.DB))
	s := model.NewZTAPIHealthStore(w.backend.Probes.DB)
	s.Now = w.config.Now
	check := ZTAPIHealthStoreProbeCheck(s, w.backend.CheckProbe)
	target := model.ZTAPIProbeTarget{ModelID: 1, Generation: 1, ConfigVersion: 1}
	for i, source := range []string{"probe", "real", "real"} {
		event := model.ZTAPIHealthEvent{ExecutionID: fmt.Sprintf("coverage-%d", i), ModelID: 1, Generation: 1, ConfigVersion: 1, CompletionSequence: uint64(i + 1), CompletedAt: now.Unix(), Source: source, Result: "success", Counted: true, Stream: i == 1}
		if i == 2 {
			event.Result, event.Counted = "unknown", false
		}
		require.NoError(t, s.DB.Create(&event).Error)
	}
	a, err := check(context.Background(), s.DB, target, now.Add(-time.Hour))
	require.NoError(t, err)
	require.False(t, a.RealCoverage)
	target.Stream = true
	a, err = check(context.Background(), s.DB, target, now.Add(-time.Hour))
	require.NoError(t, err)
	require.True(t, a.RealCoverage)
	b, err := AttachZTAPIHealthStore(w.backend, s, 999)
	require.NoError(t, err)
	job := model.ZTAPIProbeJob{ID: "probe-id", ZTAPIProbeTarget: target, Source: "probe"}
	requestID := "ztapi-health-probe-probe-id"
	request := model.ZTAPIHealthRequest{ExecutionID: "execution-final", RequestID: requestID, ModelID: 1, Generation: 1, ConfigVersion: 1, UserID: 999, Stream: true, Source: "probe", Completed: true}
	require.NoError(t, s.DB.Create(&request).Error)
	event := model.ZTAPIHealthEvent{ExecutionID: request.ExecutionID, RequestID: requestID, ModelID: 1, Generation: 1, ConfigVersion: 1, CompletionSequence: 4, Source: "probe", Stream: true, Result: "unknown"}
	require.NoError(t, s.DB.Create(&event).Error)
	final, err := b.ProbeFinalized(context.Background(), job, requestID)
	require.NoError(t, err)
	require.False(t, final)
	require.NoError(t, s.DB.Model(&event).Update("result", "success").Error)
	final, err = b.ProbeFinalized(context.Background(), job, requestID)
	require.NoError(t, err)
	require.True(t, final)
	var count int64
	require.NoError(t, s.DB.Model(&model.ZTAPIHealthEvent{}).Count(&count).Error)
	require.EqualValues(t, 4, count)
}

func TestZTAPIHealthWorkerMediaCoverageAndFinalizationMatchOperation(t *testing.T) {
	w, now, _ := healthWorkerFixture(t)
	require.NoError(t, model.MigrateZTAPIHealth(w.backend.Probes.DB))
	s := model.NewZTAPIHealthStore(w.backend.Probes.DB)
	s.Now = w.config.Now
	target := model.ZTAPIProbeTarget{ModelID: 9, Generation: 2, ConfigVersion: 3, PublicModel: "public-video", Modality: model.ZTAPIModalityVideo, Operation: types.ZTAPIHealthOperationVideoSubmit}
	inspect := func(_ context.Context, _ *gorm.DB, got model.ZTAPIProbeTarget, _ time.Time) (model.ZTAPIProbeAdmission, error) {
		return model.ZTAPIProbeAdmission{Target: got, Active: true}, nil
	}
	check := ZTAPIHealthStoreProbeCheck(s, inspect)
	for i, operation := range []string{types.ZTAPIHealthOperationVideoFetch, types.ZTAPIHealthOperationVideoSubmit} {
		event := model.ZTAPIHealthEvent{ExecutionID: fmt.Sprintf("media-coverage-%d", i), ModelID: target.ModelID, Generation: target.Generation,
			ConfigVersion: target.ConfigVersion, CompletionSequence: uint64(i + 1), CompletedAt: now.Unix(), Source: "real", Result: "success", Counted: true,
			Modality: model.ZTAPIModalityVideo, Operation: operation}
		require.NoError(t, s.DB.Create(&event).Error)
		if operation == types.ZTAPIHealthOperationVideoFetch {
			a, err := check(context.Background(), s.DB, target, now.Add(-time.Hour))
			require.NoError(t, err)
			require.False(t, a.RealCoverage, "video fetch traffic must not suppress a video submit probe")
		}
	}
	a, err := check(context.Background(), s.DB, target, now.Add(-time.Hour))
	require.NoError(t, err)
	require.True(t, a.RealCoverage)

	backend, err := AttachZTAPIHealthStore(w.backend, s, 999)
	require.NoError(t, err)
	job := model.ZTAPIProbeJob{ID: "media-final", ZTAPIProbeTarget: target, Source: "probe"}
	requestID := "ztapi-health-probe-media-final"
	request := model.ZTAPIHealthRequest{ExecutionID: "media-final-execution", RequestID: requestID, ModelID: target.ModelID, Generation: target.Generation,
		ConfigVersion: target.ConfigVersion, PublicModel: target.PublicModel, Modality: target.Modality, Operation: types.ZTAPIHealthOperationVideoFetch,
		UserID: 999, Source: "probe", Completed: true}
	require.NoError(t, s.DB.Create(&request).Error)
	event := model.ZTAPIHealthEvent{ExecutionID: request.ExecutionID, RequestID: requestID, ModelID: target.ModelID, Generation: target.Generation,
		ConfigVersion: target.ConfigVersion, PublicModel: target.PublicModel, Modality: target.Modality, Operation: types.ZTAPIHealthOperationVideoFetch,
		Source: "probe", Result: "success", Counted: true, CompletionSequence: 3}
	require.NoError(t, s.DB.Create(&event).Error)
	final, err := backend.ProbeFinalized(context.Background(), job, requestID)
	require.NoError(t, err)
	require.False(t, final, "a fetch event must not finalize a submit probe")
	require.NoError(t, s.DB.Model(&request).Updates(map[string]any{"operation": target.Operation}).Error)
	require.NoError(t, s.DB.Model(&event).Updates(map[string]any{"operation": target.Operation}).Error)
	final, err = backend.ProbeFinalized(context.Background(), job, requestID)
	require.NoError(t, err)
	require.True(t, final)
}

func TestZTAPIHealthWorkerConfigMoneyAndShutdown(t *testing.T) {
	t.Setenv("ZTAPI_HEALTH_PROBE_KEY", "")
	t.Setenv("ZTAPI_HEALTH_PROBE_USER_ID", "")
	t.Setenv("ZTAPI_HEALTH_PROBE_BUDGET_USD", "0.000000001")
	c, err := ZTAPIHealthWorkerConfigFromEnv()
	require.NoError(t, err)
	require.EqualValues(t, 1, c.InitialAllocationNanoUSD)
	require.False(t, c.ProbeIdentityValidated)
	for _, value := range []string{"-1", "1e2", "1/2", "0.0000000001", "NaN", "9223372037"} {
		t.Setenv("ZTAPI_HEALTH_PROBE_BUDGET_USD", value)
		_, err := ZTAPIHealthWorkerConfigFromEnv()
		require.Error(t, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	w, err := StartZTAPIHealthWorker(ctx, ZTAPIHealthWorkerBackend{}, DefaultZTAPIHealthWorkerConfig())
	require.NoError(t, err)
	cancel()
	select {
	case <-w.Done():
	case <-time.After(time.Second):
		t.Fatal("worker did not stop")
	}
}

func TestZTAPIHealthWorkerEngineUnpublishNoSecondACK(t *testing.T) {
	w, now, _ := healthWorkerFixture(t)
	w.config.ProbeKey = ""
	db := w.backend.Probes.DB
	require.NoError(t, model.MigrateZTAPIHealth(db))
	require.NoError(t, db.AutoMigrate(&model.ZTAPIModelConfig{}, &model.ZTAPICatalogLock{}, &model.ZTAPIAuditEvent{}))
	alias := "public-unpublish"
	c := model.ZTAPIModelConfig{SourceModel: "source-unpublish", PublicName: &alias, Published: true, Version: 7, EnabledGroups: `["default"]`}
	require.NoError(t, db.Create(&c).Error)
	incident := model.ZTAPIHealthIncident{ModelID: c.ID, Generation: 1, ConfigVersion: 7, OpenedAt: now.Unix()}
	require.NoError(t, db.Create(&incident).Error)
	require.NoError(t, db.Create(&model.ZTAPIHealthState{ModelID: c.ID, Generation: 1, Open: true, IncidentID: incident.ID}).Error)
	job := model.ZTAPIHealthOutbox{DedupKey: "incident:unpublish", Kind: "unpublish", ModelID: c.ID, Generation: 1, IncidentID: incident.ID, Status: "pending", NextAttemptAt: now.Unix()}
	require.NoError(t, db.Create(&job).Error)
	s := model.NewZTAPIHealthStore(db)
	s.Now = w.config.Now
	var err error
	w.backend, err = AttachZTAPIHealthStore(w.backend, s, 999)
	require.NoError(t, err)
	require.NoError(t, w.RunOnce(context.Background()))
	require.NoError(t, db.First(&c, c.ID).Error)
	require.False(t, c.Published)
	require.EqualValues(t, 8, c.Version)
	require.NoError(t, db.First(&job, job.ID).Error)
	require.Equal(t, "done", job.Status)
	require.NoError(t, w.RunOnce(context.Background()))
	require.NoError(t, db.First(&job, job.ID).Error)
	require.Equal(t, 1, job.Attempts)
}

func TestZTAPIHealthWorkerTimeoutAndAlertRedirect(t *testing.T) {
	config := DefaultZTAPIHealthWorkerConfig()
	config.ProbeKey = "offline"
	config.RequestTimeout = 10 * time.Millisecond
	config.ProbeTransport = healthWorkerTransport(func(r *http.Request) (*http.Response, error) { <-r.Context().Done(); return nil, r.Context().Err() })
	job := model.ZTAPIProbeJob{ZTAPIProbeTarget: model.ZTAPIProbeTarget{Protocol: "chat", PublicModel: "public"}}
	start := time.Now()
	require.Equal(t, "probe_transport_error", performZTAPIHealthProbe(context.Background(), config, job).Code)
	require.Less(t, time.Since(start), time.Second)
	calls := 0
	config.AlertWebhookURL = "https://alerts.invalid/health"
	config.AlertTransport = healthWorkerTransport(func(*http.Request) (*http.Response, error) {
		calls++
		r := workerResponse(307, "")
		r.Header.Set("Location", "https://another.invalid")
		return r, nil
	})
	accepted, code := sendZTAPIHealthAlert(context.Background(), config, ZTAPIHealthWorkItem{ID: 1})
	require.False(t, accepted)
	require.Equal(t, "alert_http_status", code)
	require.Equal(t, 1, calls)
	require.Equal(t, time.Hour, ztapiHealthAlertBackoff(999999))
}

func TestZTAPIHealthWorkerProductionDisabledStatusAndNoAllocation(t *testing.T) {
	w, _, _ := healthWorkerFixture(t)
	require.NoError(t, model.MigrateZTAPIHealth(w.backend.Probes.DB))
	oldDB, oldLog := model.DB, model.LOG_DB
	model.DB, model.LOG_DB = w.backend.Probes.DB, w.backend.Probes.DB
	t.Cleanup(func() { model.DB, model.LOG_DB = oldDB, oldLog })
	config := DefaultZTAPIHealthWorkerConfig()
	config.Now = w.config.Now
	config.InitialAllocationNanoUSD = 30_000_000_000
	backend, err := NewProductionZTAPIHealthWorkerBackend(config)
	require.NoError(t, err)
	production, err := NewZTAPIHealthWorker(backend, config)
	require.NoError(t, err)
	require.NoError(t, production.RunOnce(context.Background()))
	statuses, err := GetProductionZTAPIHealthWorkerStatus(context.Background())
	require.NoError(t, err)
	codes := []string{}
	for _, status := range statuses {
		codes = append(codes, status.Code)
	}
	require.Contains(t, codes, "probe_identity_missing")
	require.Contains(t, codes, "alert_recipient_missing_or_invalid")
	budget, err := backend.Probes.Budget(context.Background())
	require.NoError(t, err)
	require.EqualValues(t, 100_000_000, budget.AllocatedNanoUSD)
}

func productionWorkerFixture(t *testing.T) (*ZTAPIHealthWorker, *time.Time, *int) {
	t.Helper()
	w, now, sends := healthWorkerFixture(t)
	db := w.backend.Probes.DB
	require.NoError(t, model.MigrateZTAPIHealth(db))
	require.NoError(t, db.AutoMigrate(&model.Token{}, &model.User{}, &model.Log{}, &model.ZTAPIModelConfig{}, &model.ZTAPIModelPublicationSnapshot{}, &model.ZTAPIModelPriceSource{}, &model.ZTAPICatalogLock{}))
	oldDB, oldLogs := model.DB, model.LOG_DB
	model.DB, model.LOG_DB = db, db
	t.Cleanup(func() { model.DB, model.LOG_DB = oldDB, oldLogs })
	t.Setenv("ZTAPI_HEALTH_ENABLED", "true")
	t.Setenv("ZTAPI_HEALTH_PROBE_USER_ID", "999")
	require.NoError(t, db.Create(&model.User{Id: 999, Username: "offline-probe", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Quota: 1000000}).Error)
	require.NoError(t, db.Create(&model.Token{UserId: 999, KeyHash: common.HashZTAPIKey(w.config.ProbeKey), Status: common.TokenStatusEnabled, ExpiredTime: -1, RemainQuota: 1000000}).Error)
	for id := 1; id <= 3; id++ {
		alias := fmt.Sprintf("public-%d", id)
		c := model.ZTAPIModelConfig{ID: id, SourceModel: fmt.Sprintf("source-%d", id), PublicName: &alias, Published: true, Version: 1, InputCostPerMillion: 1, OutputCostPerMillion: 2, EnabledGroups: `["default"]`}
		require.NoError(t, db.Create(&c).Error)
		price := model.ZTAPIModelPriceSource{ModelConfigID: id, SourceModel: c.SourceModel, ResourceType: "enterprise", SpendTier: "default", Currency: "USD", InputPerMillion: "1", OutputPerMillion: "2", BillingDimensions: `["input_tokens","output_tokens"]`, QuotationEffectiveAt: now.Unix(), SourceDocumentChecksum: strings.Repeat("a", 64), Version: 1}
		require.NoError(t, db.Create(&price).Error)
		snapshot := model.ZTAPIModelPublicationSnapshot{ModelConfigID: id, ModelVersion: 1, SourceModel: c.SourceModel, PublicName: alias, Protocol: model.ZTAPIProtocolOpenAICompatible, EnabledGroups: `["default"]`, AllowedChannelIDs: `[1]`, PriceSourceID: price.ID, InputPricePerMillion: 1.66666667, OutputPricePerMillion: 3.33333333, CacheReadRatio: 1, CacheCreationRatio: 1, CacheCreation5mRatio: 1, CacheCreation1hRatio: 1, ImageRatio: 1, AudioRatio: 1, AudioCompletionRatio: 1}
		require.NoError(t, db.Create(&snapshot).Error)
		require.NoError(t, db.Model(&c).Update("publication_snapshot_id", snapshot.ID).Error)
	}
	backend, err := NewProductionZTAPIHealthWorkerBackend(w.config)
	require.NoError(t, err)
	w.backend = backend
	return w, now, sends
}

func TestZTAPIHealthWorkerProductionFlagFalseBlocksPaidProbe(t *testing.T) {
	w, now, sends := productionWorkerFixture(t)
	job := model.ZTAPIHealthOutbox{DedupKey: "coverage-1", Kind: "coverage", EventID: 123, Status: "pending", NextAttemptAt: now.Unix()}
	require.NoError(t, w.backend.Probes.DB.Create(&job).Error)
	t.Setenv("ZTAPI_HEALTH_ENABLED", "false")
	require.NoError(t, w.RunOnce(context.Background()))
	require.Zero(t, *sends)
	statuses, err := GetProductionZTAPIHealthWorkerStatus(context.Background())
	require.NoError(t, err)
	codes := []string{}
	for _, status := range statuses {
		codes = append(codes, status.Code)
	}
	require.Contains(t, codes, "instrumentation_disabled")
	require.Contains(t, codes, "orphaned_admission_admin_only")
	require.NoError(t, w.backend.Probes.DB.First(&job, job.ID).Error)
	require.Equal(t, "done", job.Status)
	require.Equal(t, "", job.LastError)
}

func TestZTAPIHealthWorkerProductionFairCatalogCostSamplesAndRecheck(t *testing.T) {
	w, now, _ := productionWorkerFixture(t)
	ctx := context.Background()
	first, err := w.backend.ListTargets(ctx, 1)
	require.NoError(t, err)
	require.Len(t, first, 1)
	require.Equal(t, 1, first[0].ModelID)
	require.False(t, first[0].Stream)
	require.EqualValues(t, 1_000_000_000, first[0].InputNanoUSDPerMillion)
	// A fresh backend resumes the durable mode cursor.
	b, err := NewProductionZTAPIHealthWorkerBackend(w.config)
	require.NoError(t, err)
	second, err := b.ListTargets(ctx, 1)
	require.NoError(t, err)
	require.Len(t, second, 1)
	require.Equal(t, 1, second[0].ModelID)
	require.True(t, second[0].Stream)
	third, err := b.ListTargets(ctx, 2)
	require.NoError(t, err)
	require.Len(t, third, 2)
	require.Equal(t, 2, third[0].ModelID)
	db := w.backend.Probes.DB
	for i, source := range []string{"real", "probe"} {
		requestID := fmt.Sprintf("sample-%d", i)
		require.NoError(t, db.Create(&model.ZTAPIHealthRequest{ExecutionID: requestID, RequestID: requestID, ModelID: 1, UserID: i + 10, Source: source, Completed: true}).Error)
		require.NoError(t, db.Create(&model.Log{RequestId: requestID, UserId: i + 10, ModelName: first[0].PublicModel, Type: model.LogTypeConsume, Quota: 1, PromptTokens: (i + 1) * 123, CreatedAt: now.Unix()}).Error)
	}
	job, err := dbProbeClaim(ctx, w.backend.Probes, first[0], *now)
	require.NoError(t, err)
	dispatch, sent, err := w.backend.Probes.Dispatch(ctx, job.ID, job.LeaseToken, *now, w.config.LeaseDuration, w.backend.CheckProbe)
	require.NoError(t, err)
	require.True(t, sent)
	require.EqualValues(t, 2_171_000, dispatch.ReservedNanoUSD)
	job, err = dbProbeClaim(ctx, w.backend.Probes, second[0], *now)
	require.NoError(t, err)
	t.Setenv("ZTAPI_HEALTH_ENABLED", "false")
	_, sent, err = w.backend.Probes.Dispatch(ctx, job.ID, job.LeaseToken, *now, w.config.LeaseDuration, w.backend.CheckProbe)
	require.NoError(t, err)
	require.False(t, sent)
}

func dbProbeClaim(ctx context.Context, store *model.ZTAPIProbeStore, target model.ZTAPIProbeTarget, now time.Time) (*model.ZTAPIProbeJob, error) {
	if _, err := store.Enqueue(ctx, target, now); err != nil {
		return nil, err
	}
	return store.Claim(ctx, now, 4*time.Minute)
}

func TestZTAPIHealthWorkerDoesNotRunBeforeFirstTick(t *testing.T) {
	w, _, sends := healthWorkerFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	started, err := StartZTAPIHealthWorker(ctx, w.backend, w.config)
	require.NoError(t, err)
	time.Sleep(20 * time.Millisecond)
	cancel()
	<-started.Done()
	require.Zero(t, *sends)
}

func TestZTAPIHealthWorkerAlertTimeoutCannotSuppressProbes(t *testing.T) {
	w, _, sends := healthWorkerFixture(t)
	w.config.AlertWebhookURL = "https://alerts.invalid/health"
	w.config.AlertTimeout, w.config.OutboxTimeout = 5*time.Millisecond, 15*time.Millisecond
	w.config.AlertTransport = healthWorkerTransport(func(r *http.Request) (*http.Response, error) { <-r.Context().Done(); return nil, r.Context().Err() })
	claimed := false
	w.backend.ClaimOutbox = func(_ context.Context, kind string, _ time.Time, _ time.Duration, _ int) ([]ZTAPIHealthWorkItem, error) {
		if kind != "alert" || claimed {
			return nil, nil
		}
		claimed = true
		return []ZTAPIHealthWorkItem{{ID: 1}}, nil
	}
	w.backend.FinishOutbox = func(context.Context, ZTAPIHealthWorkItem, ZTAPIHealthDelivery) error { return nil }
	w.backend.Unpublish = func(context.Context, ZTAPIHealthWorkItem) error { return nil }
	// Under load the separate outbox deadline may expire too. Reporting that
	// error is correct; the independent probe allocation must still run.
	if err := w.RunOnce(context.Background()); err != nil {
		require.ErrorIs(t, err, context.DeadlineExceeded)
	}
	require.Equal(t, 2, *sends)
}

func TestZTAPIHealthWorkerActionableAlertMetadataAllowlist(t *testing.T) {
	w, now, _ := productionWorkerFixture(t)
	db := w.backend.Probes.DB
	incident := model.ZTAPIHealthIncident{ModelID: 1, Generation: 1, PublicModel: "public-1", Rule: "consecutive_2", Failures: 2, ValidSamples: 10, ConsecutiveFailures: 2, WindowStart: now.Add(-24 * time.Hour).Unix(), OpenedAt: now.Unix(), TriggerEventID: 1}
	require.NoError(t, db.Create(&incident).Error)
	event := model.ZTAPIHealthEvent{ID: 1, ExecutionID: "alert-evidence", ModelID: 1, Generation: 1, CompletionSequence: 1, Reason: "secret-key-must-not-leak", HTTPStatus: 502, Outcome: `{"FinishReasons":["length","secret-key-must-not-leak"],"ProviderErrorCode":"secret-key-must-not-leak"}`}
	require.NoError(t, db.Create(&event).Error)
	job := model.ZTAPIHealthOutbox{DedupKey: "actionable-alert", Kind: "alert", ModelID: 1, Generation: 1, IncidentID: incident.ID, EventID: event.ID, Status: "pending", NextAttemptAt: now.Unix()}
	require.NoError(t, db.Create(&job).Error)
	items, err := w.backend.ClaimOutbox(context.Background(), "alert", *now, time.Minute, 1)
	require.NoError(t, err)
	require.Len(t, items, 1)
	w.config.AlertWebhookURL = "https://alerts.invalid/health"
	w.config.AlertTransport = healthWorkerTransport(func(r *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.NotContains(t, string(body), "secret-key-must-not-leak")
		var payload map[string]any
		require.NoError(t, common.Unmarshal(body, &payload))
		details := payload["details"].(map[string]any)
		require.Equal(t, "public-1", details["model"])
		require.Equal(t, "consecutive_2", details["rule"])
		require.EqualValues(t, 2, details["fail_count"])
		require.EqualValues(t, 10, details["valid_count"])
		require.Equal(t, false, details["unpublished"])
		require.Equal(t, "unknown", details["error_code"])
		require.Contains(t, details["finish_reasons"], "length")
		require.Equal(t, "/api/models/ztapi/1/health", details["admin_model_health_path"])
		return workerResponse(204, ""), nil
	})
	accepted, code := sendZTAPIHealthAlert(context.Background(), w.config, items[0])
	require.True(t, accepted)
	require.Equal(t, "http_2xx_accepted", code)
}

func TestZTAPIHealthWorkerEveryTickHeartbeatDoesNotClearFaults(t *testing.T) {
	w, now, _ := productionWorkerFixture(t)
	t.Setenv("ZTAPI_HEALTH_ENABLED", "false")
	seen := []string{}
	persist := w.backend.OperationalStatus
	w.backend.OperationalStatus = func(ctx context.Context, code, ref string) { seen = append(seen, code); persist(ctx, code, ref) }
	require.NoError(t, w.RunOnce(context.Background()))
	require.Equal(t, "worker_tick_running", seen[0])
	require.Equal(t, "worker_ready", seen[len(seen)-1])
	*now = now.Add(time.Minute)
	require.NoError(t, w.RunOnce(context.Background()))
	statuses, err := GetProductionZTAPIHealthWorkerStatus(context.Background())
	require.NoError(t, err)
	byComponent := map[string]model.ZTAPIHealthWorkerStatus{}
	for _, status := range statuses {
		byComponent[status.Component] = status
	}
	require.Equal(t, "worker_ready", byComponent["worker"].Code)
	require.Equal(t, now.Unix(), byComponent["worker"].UpdatedAt)
	require.Equal(t, "instrumentation_disabled", byComponent["probe"].Code)
	require.Equal(t, "alert_recipient_missing_or_invalid", byComponent["alert"].Code)
	w.backend.ClaimOutbox = func(context.Context, string, time.Time, time.Duration, int) ([]ZTAPIHealthWorkItem, error) {
		return nil, errors.New("synthetic database failure")
	}
	require.Error(t, w.RunOnce(context.Background()))
	require.Equal(t, "worker_tick_error", seen[len(seen)-1])
}

func TestZTAPIHealthWorkerProductionRequiresOrdinaryProbeUser(t *testing.T) {
	w, _, sends := productionWorkerFixture(t)
	ctx := context.Background()
	valid, code, err := w.backend.ValidateProbeIdentity(ctx)
	require.NoError(t, err)
	require.True(t, valid)
	require.Equal(t, "probe_ready", code)
	for _, role := range []int{common.RoleGuestUser, common.RoleSupportUser, common.RoleFinanceUser, common.RoleAdminUser, common.RoleRootUser, 999} {
		t.Run(fmt.Sprint(role), func(t *testing.T) {
			require.NoError(t, w.backend.Probes.DB.Model(&model.User{}).Where("id = ?", 999).Update("role", role).Error)
			valid, code, err := w.backend.ValidateProbeIdentity(ctx)
			require.NoError(t, err)
			require.False(t, valid)
			require.Equal(t, "probe_identity_invalid", code)
			require.NoError(t, w.RunOnce(ctx))
			require.Zero(t, *sends)
		})
	}
	budget, err := w.backend.Probes.Budget(ctx)
	require.NoError(t, err)
	require.Zero(t, budget.AccountedNanoUSD)
	var user model.User
	require.NoError(t, w.backend.Probes.DB.First(&user, 999).Error)
	require.Equal(t, 1000000, user.Quota)
	var token model.Token
	require.NoError(t, w.backend.Probes.DB.Where("user_id = ?", 999).First(&token).Error)
	require.Equal(t, 1000000, token.RemainQuota)
}

func TestZTAPIHealthWorkerProductionRoleChangeCancelsBeforeDispatch(t *testing.T) {
	w, now, sends := productionWorkerFixture(t)
	ctx := context.Background()
	targets, err := w.backend.ListTargets(ctx, 1)
	require.NoError(t, err)
	require.Len(t, targets, 1)
	job, err := dbProbeClaim(ctx, w.backend.Probes, targets[0], *now)
	require.NoError(t, err)
	require.NotNil(t, job)
	require.NoError(t, w.backend.Probes.DB.Model(&model.User{}).Where("id = ?", 999).Update("role", common.RoleAdminUser).Error)
	result, send, err := w.backend.Probes.Dispatch(ctx, job.ID, job.LeaseToken, *now, w.config.LeaseDuration, w.backend.CheckProbe)
	require.NoError(t, err)
	require.False(t, send)
	require.Equal(t, "cancelled", result.State)
	require.Zero(t, *sends)
	budget, err := w.backend.Probes.Budget(ctx)
	require.NoError(t, err)
	require.Zero(t, budget.AccountedNanoUSD)
	var status model.ZTAPIHealthWorkerStatus
	require.NoError(t, w.backend.Probes.DB.First(&status, "component = ?", "probe").Error)
	require.Equal(t, "probe_identity_invalid", status.Code)
}
