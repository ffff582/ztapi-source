package finance

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
)

func customerServiceCost(t *testing.T, extra string) TestCost {
	t.Helper()
	var c TestCost
	require.NoError(t, common.UnmarshalJsonStr(`{"request_id":"r1","attempt":1,"channel_id":2,"purpose":"customer_service","input_tokens":100,"output_tokens":0,"input_token_scope":"inclusive","cache_read_tokens":20,"cache_write_5m_tokens":0,"cache_write_1h_tokens":0,"status":"confirmed","evidence_type":"supplier_statement","coverage":"included","amount":"2","refund":"0","currency":"USDT","rate":"1","evidence":"supplier service receipt","coverage_evidence":"invoice-1"`+extra+`}`, &c))
	return c
}

func reportMap(t *testing.T, r Report) map[string]any {
	t.Helper()
	b, err := common.Marshal(r)
	require.NoError(t, err)
	var result map[string]any
	require.NoError(t, common.Unmarshal(b, &result))
	return result
}

func TestCustomerServiceCostSeparateFromTestsWithoutDoubleDeduction(t *testing.T) {
	for _, coverage := range []string{"included", "extra", "unknown"} {
		t.Run(coverage, func(t *testing.T) {
			s := fixture()
			s.Users[0].Role = common.RoleAdminUser
			c := customerServiceCost(t, "")
			c.Coverage = coverage
			if coverage != "included" {
				c.CoverageEvidence = "outside monthly bill"
			}
			before, err := common.Marshal(s)
			require.NoError(t, err)
			r := calculate(t, s, append(facts(), revenueTestFact(c)))
			m := reportMap(t, r)
			require.Empty(t, r.TestCosts)
			require.Equal(t, "0.000000", r.TestConfirmedTotal)
			require.Equal(t, "2.000000", m["customer_service_confirmed_total"])
			rows, ok := m["customer_service_costs"].([]any)
			require.True(t, ok)
			require.Len(t, rows, 1)
			row := rows[0].(map[string]any)
			require.Equal(t, float64(0), row["output_tokens"])
			require.Equal(t, float64(20), row["cache_read_tokens"])
			require.Equal(t, "0.000000", r.KnownRevenue)
			if coverage == "unknown" {
				require.Nil(t, r.ActualCost)
				require.Nil(t, r.GrossProfit)
			} else {
				want := "15.000000"
				if coverage == "extra" {
					want = "17.000000"
				}
				require.Equal(t, want, *r.ActualCost)
				require.Equal(t, "-"+want, *r.GrossProfit)
			}
			after, err := common.Marshal(s)
			require.NoError(t, err)
			require.Equal(t, string(before), string(after))
		})
	}
}

func TestCustomerServiceCostCannotHideOrdinaryCustomerRevenue(t *testing.T) {
	s := fixture()
	c := customerServiceCost(t, "")
	in := Submission{Kind: "test_cost", Ref: "test:r1:1:2", TestCost: &c, IdempotencyKey: "customer-service-source"}
	require.False(t, validSourceReference(s, in))
	r := calculate(t, s, append(facts(), revenueTestFact(c)))
	require.Equal(t, "20.000000", r.KnownRevenue)
	require.Empty(t, reportMap(t, r)["customer_service_costs"])
	require.Nil(t, r.GrossProfit)
}

func TestCustomerServiceCostValidation(t *testing.T) {
	for name, extra := range map[string]string{
		"unknown purpose":   `,"purpose":"customer"`,
		"negative cache":    `,"cache_read_tokens":-1`,
		"inclusive overlap": `,"cache_read_tokens":101`,
		"missing scope":     `,"input_token_scope":""`,
		"unknown scope":     `,"input_token_scope":"guess"`,
	} {
		t.Run(name, func(t *testing.T) {
			c := customerServiceCost(t, extra)
			require.Error(t, validateSubmission(Submission{Kind: "test_cost", Ref: "test:r1:1:2", TestCost: &c, IdempotencyKey: "customer-service-invalid"}, time.Now()))
		})
	}
}

func TestCustomerServiceCostStoreRetainsPurposeAndCache(t *testing.T) {
	s := newStore(t)
	c := customerServiceCost(t, "")
	in := Submission{Kind: "test_cost", Ref: "test:r1:1:2", TestCost: &c, IdempotencyKey: "customer-service-roundtrip"}
	f, err := s.Append(context.Background(), in, 1, time.Now())
	require.NoError(t, err)
	again, err := s.Append(context.Background(), in, 1, time.Now())
	require.NoError(t, err)
	require.Equal(t, f.ID, again.ID)
	b, err := common.Marshal(again.Payload)
	require.NoError(t, err)
	require.Contains(t, string(b), `"purpose":"customer_service"`)
	require.Contains(t, string(b), `"cache_read_tokens":20`)
}

func TestCustomerServiceCostCSVCategory(t *testing.T) {
	s := fixture()
	s.Users[0].Role = common.RoleAdminUser
	r := calculate(t, s, append(facts(), revenueTestFact(customerServiceCost(t, ""))))
	w := httptest.NewRecorder()
	exportReport(w, r)
	require.Contains(t, w.Body.String(), "customer_service_cost")
	require.Contains(t, w.Body.String(), "customer_service_confirmed_total")
}

func TestCustomerServiceCostCreditConversionAndEstimate(t *testing.T) {
	s := fixture()
	s.Users[0].Role = common.RoleAdminUser
	c := customerServiceCost(t, "")
	c.Currency, c.Rate, c.RateEvidence = "XCLOUD_CREDIT", "0.5", "paid 100 U for 200 credits"
	c.Amount = "4"
	r := calculate(t, s, append(facts(), revenueTestFact(c)))
	m := reportMap(t, r)
	require.Equal(t, "2.000000", m["customer_service_confirmed_total"])
	require.Equal(t, "4.00000000", m["customer_service_xcloud_credit_total"])
	require.Equal(t, "0.00000000", r.TestXcloudCreditTotal)
	require.Equal(t, "15.000000", *r.ActualCost)
	c.Currency, c.Rate, c.Status, c.EvidenceType = "USDT", "1", "estimate", "quote"
	c.Amount, c.Coverage, c.CoverageEvidence = "0.03", "unknown", ""
	r = calculate(t, s, append(facts(), revenueTestFact(c)))
	m = reportMap(t, r)
	require.Equal(t, "0.030000", m["customer_service_estimate_total"])
	require.Equal(t, "0.000000", m["customer_service_confirmed_total"])
	require.Nil(t, r.GrossProfit)
	require.Equal(t, "15.000000", r.RecordedCost)
}

func TestCustomerServiceCostHTTPRequiresInternalOwnerAndSameOrigin(t *testing.T) {
	h, token := api(t, common.RoleRootUser)
	c := customerServiceCost(t, "")
	b, err := common.Marshal(Submission{Kind: "test_cost", Ref: "test:r1:1:2", TestCost: &c, IdempotencyKey: "customer-service-http"})
	require.NoError(t, err)
	body := string(b)
	require.Equal(t, 403, request(h, token, "POST", prefix+"facts", body, "https://evil.example").Code)
	require.Equal(t, 400, request(h, token, "POST", prefix+"facts", body, "https://admin.example.test").Code)
	h.(*handler).source.(*testSource).s.Users[0].Role = common.RoleAdminUser
	require.Equal(t, 201, request(h, token, "POST", prefix+"facts", body, "https://admin.example.test").Code)
	w := request(h, token, "GET", prefix+"report?month=2026-09", "", "")
	require.Equal(t, 200, w.Code)
	require.Contains(t, w.Body.String(), `"customer_service_costs":[{`)
	require.NotContains(t, w.Body.String(), `"test_costs":[{`)
	// The classification endpoint remains finance-only, never a customer tool.
	h.(*handler).source.(*testSource).role = common.RoleSupportUser
	require.Equal(t, 403, request(h, token, "GET", prefix+"report?month=2026-09", "", "").Code)
	require.Equal(t, 403, request(h, token, "POST", prefix+"facts", body, "https://admin.example.test").Code)
	require.NotContains(t, strings.ToLower(w.Body.String()), "api_key")
}

func TestCustomerServiceToolsDoNotPolluteCustomerOrManagementTests(t *testing.T) {
	s, f := hostedToolFixture("search")
	s.Users[0].Role = common.RoleAdminUser
	fact := hostedToolTestFact("confirmed", "included")
	c := fact.Payload.(TestCost)
	c.Purpose = customerServicePurpose
	fact.Payload = c
	f = append(f, fact)
	r := calculate(t, s, f)
	require.Empty(t, r.HostedTools, "customer-service tools are reported only in their own cost category")
	require.Empty(t, r.TestCosts)
	require.Len(t, r.CustomerServiceCosts, 1)
}

func TestCustomerServiceCostRevisionCannotLosePurpose(t *testing.T) {
	s := newStore(t)
	c := customerServiceCost(t, "")
	in := Submission{Kind: "test_cost", Ref: "test:r1:1:2", TestCost: &c, IdempotencyKey: "customer-service-purpose-original"}
	f, err := s.Append(context.Background(), in, 1, time.Now())
	require.NoError(t, err)
	in.PreviousID = f.ID
	in.IdempotencyKey = "customer-service-purpose-stripped"
	in.TestCost.Purpose = ""
	_, err = s.Append(context.Background(), in, 1, time.Now())
	require.ErrorIs(t, err, ErrConflict, "an older editor must not silently reclassify customer service as testing")
}
