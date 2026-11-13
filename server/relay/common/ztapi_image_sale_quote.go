package common

import "github.com/QuantumNous/new-api/types"

func (info *RelayInfo) MarkZTAPIImageDeliveryFailed() {
	if info != nil {
		info.ztapiImageResponseMu.Lock()
		defer info.ztapiImageResponseMu.Unlock()
		info.ztapiImageDeliveryFailed = true
	}
}
func (info *RelayInfo) ZTAPIImageDeliveryFailed() bool {
	if info == nil {
		return false
	}
	info.ztapiImageResponseMu.Lock()
	defer info.ztapiImageResponseMu.Unlock()
	return info.ztapiImageDeliveryFailed
}

func (info *RelayInfo) CaptureZTAPIImageSaleQuote(quote types.ZTAPIImageSaleQuote, inputSHA256 string) bool {
	if info == nil || info.ztapiImageSaleQuote != nil || len(inputSHA256) != 64 {
		return false
	}
	copy := quote
	info.ztapiImageSaleQuote, info.ztapiImageSaleInputSHA256 = &copy, inputSHA256
	return true
}

func (info *RelayInfo) GetZTAPIImageSaleQuote() (*types.ZTAPIImageSaleQuote, string) {
	if info == nil || info.ztapiImageSaleQuote == nil {
		return nil, ""
	}
	copy := *info.ztapiImageSaleQuote
	return &copy, info.ztapiImageSaleInputSHA256
}
