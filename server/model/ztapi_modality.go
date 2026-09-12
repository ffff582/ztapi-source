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
	return ZTAPIModalityText
}
