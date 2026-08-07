package aws

// SSORegions lists the AWS commercial regions where IAM Identity Center's OIDC endpoint
// exists — that is, every value that is valid for sso_region.
//
// This is a snapshot, not a live lookup. Unauthenticated sources do exist, but neither is
// better than the SDK table, which was measured on 2026-08-07:
//
//   - api.regional-table.region-services.aws.a2z.com carries per-service availability, but
//     its own metadata says "intended for use only on aws.amazon.com. We do not guarantee
//     its availability or accuracy", and its data was then nine months old — staler than
//     this file. It agreed exactly with the list below on commercial regions.
//   - ip-ranges.amazonaws.com is supported and updated daily, but enumerates all regions
//     rather than the ones Identity Center runs in, so it would offer regions where sign-in
//     cannot work.
//
// Either would still need this list as an offline fallback, making them additive rather
// than a replacement. The list is copied from the SDK's own endpoint table, which is the
// authoritative source and already a dependency:
//
//	github.com/aws/aws-sdk-go-v2/service/ssooidc/internal/endpoints
//
// It cannot be imported (Go's internal/ rule confines it to the SDK module), so it is
// vendored here instead. AWS adds regions a few times a year, so refresh it when bumping
// the SDK by re-reading the "aws" partition block of that file.
//
// Listed partitions are aws (commercial) and aws-us-gov. Identity Center also runs in
// aws-cn (cn-north-1, cn-northwest-1) and aws-eusc (eusc-de-east-1); both were left out
// deliberately — China needs a separate account structure entirely and EU Sovereign is new
// enough to be noise in the picker. Add them here if a client ever lands in one.
//
// Staleness is a convenience problem, never a blocker: the setup form takes free text, so a
// region missing from this list can always be typed by hand.
var SSORegions = []string{
	"af-south-1",
	"ap-east-1",
	"ap-east-2",
	"ap-northeast-1",
	"ap-northeast-2",
	"ap-northeast-3",
	"ap-south-1",
	"ap-south-2",
	"ap-southeast-1",
	"ap-southeast-2",
	"ap-southeast-3",
	"ap-southeast-4",
	"ap-southeast-5",
	"ap-southeast-6",
	"ap-southeast-7",
	"ca-central-1",
	"ca-west-1",
	"eu-central-1",
	"eu-central-2",
	"eu-north-1",
	"eu-south-1",
	"eu-south-2",
	"eu-west-1",
	"eu-west-2",
	"eu-west-3",
	"il-central-1",
	"me-central-1",
	"me-south-1",
	"mx-central-1",
	"sa-east-1",
	"us-east-1",
	"us-east-2",
	// GovCloud is a separate partition, but sso_region takes a plain region name and the
	// SDK infers the partition from it, so these need no special handling beyond listing.
	"us-gov-east-1",
	"us-gov-west-1",
	"us-west-1",
	"us-west-2",
}
