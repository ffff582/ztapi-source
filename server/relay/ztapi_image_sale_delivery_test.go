package relay

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestZTAPIImageSalePartialDirectRequestsNeverReserveOrDispatch(t *testing.T) {
	service.InitHttpClient()
	service.InitTokenEncoders()
	for _, scenario := range []string{"unaccepted_quality", "unaccepted_size", "multiple_images"} {
		t.Run(scenario, func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.WriteHeader(http.StatusInternalServerError)
			}))
			defer upstream.Close()
			body := validManagedImageBody()
			if scenario == "unaccepted_size" {
				body["size"] = "2048x2048"
			}
			if scenario == "multiple_images" {
				body["n"] = 2
			}
			c, info := managedImageFixture(t, http.MethodPost, "/v1/images/generations", body)
			info.RelayMode = relayconstant.RelayModeImagesGenerations
			common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, upstream.URL)
			db := setupZTAPIImageBillingChain(t, c, info, 200000, -1)
			info.ZTAPIPublicationSnapshot.ImageSaleContractJSON = types.ZTAPIImageSalePartial20261109JSON()
			info.ZTAPIPublicationSnapshot.ImageProtocolContract.Capabilities.RenderOptions = []types.ZTAPIImageRenderOption{{Size: body["size"].(string), Quality: "standard", Resolution: "1K"}}
			require.Error(t, service.PrepareZTAPIImageSaleQuote(c, info))
			// Even a direct helper invocation without a quote cannot dispatch.
			require.NotNil(t, ImageHelper(c, info))
			require.Zero(t, calls.Load())
			var count int64
			require.NoError(t, db.Model(&model.ZTAPIRequestSettlement{}).Count(&count).Error)
			require.Zero(t, count)
			require.NoError(t, db.Model(&model.ZTAPIRequestAttempt{}).Count(&count).Error)
			require.Zero(t, count)
			var user model.User
			require.NoError(t, db.First(&user, info.UserId).Error)
			require.Equal(t, 200000, user.Quota)
		})
	}
}

type closedImageSaleWriter struct{ gin.ResponseWriter }

func (w *closedImageSaleWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestZTAPIImageSaleIncompleteDeliveryNeverSettlesOrAutoRefunds(t *testing.T) {
	for _, flat := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "per_picture"}[flat], func(t *testing.T) {
			service.InitHttpClient()
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"request_id":"req-image-delivery","data":[{"url":"https://example.invalid/x"}],"usage":{"input_tokens":10,"output_tokens":7,"total_tokens":17}}`)
			}))
			defer upstream.Close()
			c, info := managedImageFixture(t, http.MethodPost, "/v1/images/generations", validManagedImageBody())
			info.RelayMode = relayconstant.RelayModeImagesGenerations
			common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, upstream.URL)
			db := setupZTAPIImageBillingChain(t, c, info, 150000, -1)
			reserve := 20
			if flat {
				info.ZTAPIPublicationSnapshot.ImageSaleContractJSON = types.ZTAPIImageSaleContract20261109JSON()
				protocol := info.ZTAPIPublicationSnapshot.ImageProtocolContract.Clone()
				protocol.Capabilities.RenderOptions = []types.ZTAPIImageRenderOption{{AspectRatio: "1:1", Resolution: "1K", Size: "1024x1024", Quality: "standard"}}
				filtered := protocol.Reservations[:0]
				for _, authority := range protocol.Reservations {
					if authority.Size == "1024x1024" && authority.Quality == "standard" {
						filtered = append(filtered, authority)
					}
				}
				protocol.Reservations = filtered
				protocol, _, err := types.SealZTAPIImageProtocolContract(protocol)
				require.NoError(t, err)
				info.ZTAPIPublicationSnapshot.ImageProtocolContract = &protocol
				require.NoError(t, service.PrepareZTAPIImageSaleQuote(c, info))
				reserve = 150000
			}
			require.Nil(t, service.PreConsumeBilling(c, reserve, info))
			reserve = info.Billing.GetPreConsumedQuota()
			c.Writer = &closedImageSaleWriter{c.Writer}
			require.Nil(t, ImageHelper(c, info))
			require.True(t, info.ZTAPIImageDeliveryFailed())
			var row model.ZTAPIRequestSettlement
			require.NoError(t, db.Where("request_id = ?", info.RequestId).First(&row).Error)
			require.Equal(t, model.ZTAPISettlementPending, row.Status)
			require.Contains(t, row.MissingDimensionsJSON, "image_delivery_incomplete")
			require.EqualValues(t, reserve, row.ReservedQuota)
			require.Zero(t, row.ChargedQuota)
			require.NoError(t, info.Billing.Refund(c))
			var user model.User
			require.NoError(t, db.First(&user, info.UserId).Error)
			require.Equal(t, 150000-reserve, user.Quota)
		})
	}
}
