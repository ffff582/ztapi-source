package finance

import "github.com/QuantumNous/new-api/types"

type ImageSaleReferralQuoteItem = types.ImageSaleReferralQuoteItem
type ImageSaleReferralQuote = types.ImageSaleReferralQuote

func QuoteImageSaleReferralCosts(retailJSON, sourceCostJSON, operation string) (ImageSaleReferralQuote, error) {
	return types.QuoteImageSaleReferralCosts(retailJSON, sourceCostJSON, operation)
}
