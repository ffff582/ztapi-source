package model

const (
	ZTAPIModalityText      = "text"
	ZTAPIModalityEmbedding = "embedding"
	ZTAPIModalityImage     = "image"
	ZTAPIModalityVideo     = "video"
)

// The checked-in quotation, not a mutable alias or a name prefix, owns modality.
func ZTAPIModelModality(sourceModel string) string {
	if ztapiQuotationLoadError == nil {
		for _, entry := range ztapiQuotation.Entries {
			if entry.Status == "mapped" && entry.SourceModel == sourceModel {
				return entry.Modality
			}
		}
	}
	// A model the A/B workbook introduces has no row in the first quotation, so
	// its modality comes from the identity that workbook declares. Without this
	// a video model would be billed and routed as text.
	for _, identity := range ztapiABIntroducedIdentityClaims {
		if identity.SourceModel == sourceModel {
			return identity.Modality
		}
	}
	// A declared media candidate must enter media admission and metering even
	// before it is publishable. Inferring this from arbitrary suffixes is unsafe.
	if candidates, err := loadZTAPISupplierAdmissionModels20261019(); err == nil {
		for _, candidate := range candidates {
			if candidate.SourceModel != "" && candidate.SourceModel == sourceModel {
				return candidate.Modality
			}
		}
	}
	return ZTAPIModalityText
}
