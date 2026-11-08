package service

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"math"
	"strconv"

	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	_ "golang.org/x/image/webp"
)

func ztapiImageSaleResolution(protocol types.ZTAPIImageProtocolContract, selector types.ZTAPIImageSelector) (string, error) {
	for _, option := range protocol.Capabilities.RenderOptions {
		if option.Size == selector.Size && option.Quality == selector.Quality {
			return option.Resolution, nil
		}
	}
	return "", errors.New("image size has no published retail resolution")
}

// Called only after protocol admission. It never fetches URLs or decodes pixels.
func PrepareZTAPIImageSaleQuote(c *gin.Context, info *relaycommon.RelayInfo) error {
	if info == nil || info.ZTAPIPublicationSnapshot == nil || info.ZTAPIPublicationSnapshot.ImageSaleContractJSON == "" {
		return nil
	}
	quote, digest, err := computeZTAPIImageSaleQuote(c, info)
	if err != nil {
		return err
	}
	if !info.CaptureZTAPIImageSaleQuote(quote, digest) {
		return errors.New("image quote is already frozen")
	}
	c.Header("X-ZTAPI-Image-Quote-USD", quote.TotalUSD)
	return nil
}

func VerifyZTAPIImageSaleQuote(c *gin.Context, info *relaycommon.RelayInfo) error {
	if info == nil || info.ZTAPIPublicationSnapshot == nil || info.ZTAPIPublicationSnapshot.ImageSaleContractJSON == "" {
		return nil
	}
	quote, digest, err := computeZTAPIImageSaleQuote(c, info)
	frozen, frozenDigest := info.GetZTAPIImageSaleQuote()
	if err != nil || frozen == nil || quote != *frozen || digest != frozenDigest {
		return errors.New("image request differs from its frozen retail quote")
	}
	return nil
}

func computeZTAPIImageSaleQuote(c *gin.Context, info *relaycommon.RelayInfo) (result types.ZTAPIImageSaleQuote, digest string, err error) {
	snapshot := info.ZTAPIPublicationSnapshot
	request, ok := info.Request.(*dto.ImageRequest)
	if !ok || request == nil || snapshot.ImageProtocolContract == nil {
		return result, "", model.ErrZTAPISettlementInvalid
	}
	selectorJSON, err := ztapiImageReservationSelector(request, info)
	if err != nil {
		return result, "", err
	}
	selector, _, err := canonicalZTAPIMediaSelector(selectorJSON)
	if err != nil {
		return result, "", err
	}
	resolution, err := ztapiImageSaleResolution(*snapshot.ImageProtocolContract, selector)
	if err != nil {
		return result, "", err
	}
	var referenceUnits int64
	fileDigests := []string{}
	if selector.Operation == "edit" {
		edit := snapshot.ImageProtocolContract.Edit
		if c == nil || c.Request == nil || c.Request.MultipartForm == nil || edit == nil {
			return result, "", model.ErrZTAPISettlementInvalid
		}
		files := c.Request.MultipartForm.File[edit.InputField]
		if len(files) == 0 || len(files) > edit.MaxInputFiles {
			return result, "", model.ErrZTAPISettlementInvalid
		}
		for _, header := range files {
			if header == nil || header.Size <= 0 || header.Size == math.MaxInt64 || (edit.MaxInputBytes > 0 && header.Size > edit.MaxInputBytes) {
				return result, "", model.ErrZTAPISettlementInvalid
			}
			file, openErr := header.Open()
			if openErr != nil {
				return result, "", errors.New("cannot inspect reference image")
			}
			config, _, decodeErr := image.DecodeConfig(io.LimitReader(file, header.Size))
			if decodeErr != nil {
				_ = file.Close()
				return result, "", errors.New("reference image dimensions cannot be verified")
			}
			hash := sha256.New()
			written, hashErr := io.Copy(hash, io.NewSectionReader(file, 0, header.Size+1))
			_ = file.Close()
			if hashErr != nil || written != header.Size {
				return result, "", errors.New("reference image changed during quoting")
			}
			fileDigests = append(fileDigests, hex.EncodeToString(hash.Sum(nil)))
			units, unitsErr := types.ZTAPIImageReferenceUnits(int64(config.Width), int64(config.Height))
			if unitsErr != nil || referenceUnits > math.MaxInt32-units {
				return result, "", errors.New("reference image dimensions exceed billing range")
			}
			referenceUnits += units
		}
	}
	quote, err := types.QuoteZTAPIImageSale(snapshot.ImageSaleContractJSON, resolution, int64(CountTextToken(request.Prompt, snapshot.SourceModel)), referenceUnits, int64(selector.N))
	if err != nil {
		return result, "", err
	}
	digest, err = imageSaleInputFingerprint(request.Prompt, selectorJSON, fileDigests)
	return quote, digest, err
}

func deriveZTAPIImageSaleReservation(raw string, protocol types.ZTAPIImageProtocolContract, selector types.ZTAPIImageSelector, quote *types.ZTAPIImageSaleQuote, quotaPerUnit decimal.Decimal) (map[string]string, int64, error) {
	canonical, err := types.CanonicalizeZTAPIImageSaleContract(raw)
	if err != nil || canonical != raw || quote == nil || types.ValidateZTAPIImageSaleQuote(raw, *quote) != nil || quote.Images != int64(selector.N) || !quotaPerUnit.IsPositive() {
		return nil, 0, model.ErrZTAPISettlementInvalid
	}
	resolution, err := ztapiImageSaleResolution(protocol, selector)
	if err != nil || resolution != quote.Resolution {
		return nil, 0, model.ErrZTAPISettlementInvalid
	}
	if _, ok := protocol.FindReservationAuthority(selector); !ok {
		return nil, 0, model.ErrZTAPISettlementInvalid
	}
	if selector.Operation == "edit" {
		if quote.ReferenceUnits < 1 {
			return nil, 0, model.ErrZTAPISettlementInvalid
		}
	} else if quote.ReferenceUnits != 0 {
		return nil, 0, model.ErrZTAPISettlementInvalid
	}
	quota := decimal.RequireFromString(quote.TotalUSD).Mul(quotaPerUnit)
	if quota.IsNegative() || !quota.Equal(quota.Floor()) || quota.GreaterThan(decimal.NewFromInt(math.MaxInt32)) {
		return nil, 0, model.ErrZTAPISettlementInvalid
	}
	return map[string]string{"images": strconv.FormatInt(quote.Images, 10)}, quota.IntPart(), nil
}
