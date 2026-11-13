package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
)

func imageSaleInputFingerprint(prompt, selectorJSON string, fileDigests []string) (string, error) {
	encoded, err := common.Marshal(struct {
		Prompt, Selector string
		Files            []string
	}{prompt, selectorJSON, fileDigests})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

// Validate the exact immutable bytes used by NewOutboundBody, not a file that
// will be opened again after the price check.
func VerifyZTAPIImageSaleDispatch(info *relaycommon.RelayInfo, dispatch *relaycommon.ZTAPIManagedImageDispatch) error {
	if info == nil || info.ZTAPIPublicationSnapshot == nil || info.ZTAPIPublicationSnapshot.ImageSaleContractJSON == "" {
		return nil
	}
	invalid := errors.New("image dispatch differs from its frozen retail quote")
	quote, wantHash := info.GetZTAPIImageSaleQuote()
	request, ok := info.Request.(*dto.ImageRequest)
	if !ok || request == nil || quote == nil || dispatch == nil || info.ZTAPIPublicationSnapshot.ImageProtocolContract == nil {
		return invalid
	}
	selectorJSON, err := ztapiImageReservationSelector(request, info)
	if err != nil {
		return invalid
	}
	selector, _, err := canonicalZTAPIMediaSelector(selectorJSON)
	if err != nil {
		return invalid
	}
	protocol := info.ZTAPIPublicationSnapshot.ImageProtocolContract
	fields := map[string]string{}
	files := []string{}
	policies := protocol.UpstreamRequestFields
	if selector.Operation == "edit" {
		edit := protocol.Edit
		if edit == nil || dispatch.ProviderPath != edit.ProviderPath {
			return invalid
		}
		policies = edit.RequestFields
		mediaType, params, err := mime.ParseMediaType(dispatch.ContentType)
		if err != nil || mediaType != "multipart/form-data" || params["boundary"] == "" {
			return invalid
		}
		reader := multipart.NewReader(bytes.NewReader(dispatch.Body), params["boundary"])
		var total int64
		for {
			part, err := reader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				return invalid
			}
			if part.FileName() != "" {
				if part.FormName() != edit.InputField || len(files) >= edit.MaxInputFiles {
					return invalid
				}
				limit := edit.MaxInputBytes
				if limit <= 0 || limit > 256<<20 {
					return invalid
				}
				hash := sha256.New()
				n, err := io.Copy(hash, io.LimitReader(part, limit+1))
				_ = part.Close()
				if err != nil || n <= 0 || n > limit || total > edit.MaxTotalInputBytes-n {
					return invalid
				}
				total += n
				files = append(files, hex.EncodeToString(hash.Sum(nil)))
			} else {
				name := part.FormName()
				if _, seen := fields[name]; seen {
					return invalid
				}
				value, err := io.ReadAll(io.LimitReader(part, (32<<20)+1))
				_ = part.Close()
				if err != nil || len(value) > 32<<20 {
					return invalid
				}
				fields[name] = string(value)
			}
		}
		if len(files) == 0 {
			return invalid
		}
	} else {
		if dispatch.ProviderPath != protocol.Path || common.RejectDuplicateJsonObjectMembers(bytes.NewReader(dispatch.Body)) != nil {
			return invalid
		}
		var object map[string]json.RawMessage
		if common.Unmarshal(dispatch.Body, &object) != nil {
			return invalid
		}
		for key, value := range object {
			if key == "n" {
				fields[key] = string(value)
				continue
			}
			var text string
			if common.Unmarshal(value, &text) != nil {
				return invalid
			}
			fields[key] = text
		}
	}
	expected := map[string]string{"model": protocol.ProviderModel, "prompt": request.Prompt, "n": strconv.Itoa(selector.N), "size": selector.Size, "quality": selector.Quality, "response_format": selector.ResponseFormat}
	for key, value := range fields {
		want, known := expected[key]
		if !known || policies[key] == types.ZTAPIImageRequestFieldOmit || value != want {
			return invalid
		}
	}
	for key := range expected {
		if policies[key] != types.ZTAPIImageRequestFieldOmit {
			if _, ok := fields[key]; !ok {
				return invalid
			}
		}
	}
	hash, err := imageSaleInputFingerprint(fields["prompt"], selectorJSON, files)
	if err != nil || hash != wantHash {
		return invalid
	}
	return nil
}
