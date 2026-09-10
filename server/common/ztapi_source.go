package common

import "os"

const (
	defaultZTAPISourceRepository  = "https://github.com/ffff582/ztapi-source"
	ztapiSourceLicense            = "AGPL-3.0-or-later"
	ztapiUpstreamSourceRepository = "https://github.com/QuantumNous/new-api"
)

type ZTAPISourceMetadata struct {
	License            string `json:"license"`
	SourceRepository   string `json:"source_repository"`
	ProductionCommit   string `json:"production_commit"`
	SourceTag          string `json:"source_tag"`
	SourceCommit       string `json:"source_commit"`
	SourceURL          string `json:"source_url"`
	UpstreamRepository string `json:"upstream_repository"`
}

func GetZTAPISourceMetadata() ZTAPISourceMetadata {
	productionCommit := os.Getenv("ZTAPI_RELEASE_VERSION")
	if productionCommit == "" {
		productionCommit = Version
	}
	sourceRepository := GetEnvOrDefaultString(
		"ZTAPI_SOURCE_REPOSITORY",
		defaultZTAPISourceRepository,
	)
	sourceTag := os.Getenv("ZTAPI_SOURCE_TAG")
	if sourceTag == "" {
		sourceTag = "production-" + productionCommit
	}
	sourceCommit := os.Getenv("ZTAPI_SOURCE_COMMIT")
	sourceURL := sourceRepository
	if sourceCommit != "" {
		sourceURL += "/tree/" + sourceCommit
	}

	return ZTAPISourceMetadata{
		License:            ztapiSourceLicense,
		SourceRepository:   sourceRepository,
		ProductionCommit:   productionCommit,
		SourceTag:          sourceTag,
		SourceCommit:       sourceCommit,
		SourceURL:          sourceURL,
		UpstreamRepository: ztapiUpstreamSourceRepository,
	}
}
