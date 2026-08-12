package aws

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

// Instance is a running EC2 instance.
type Instance struct {
	ID        string
	Name      string
	PrivateIP string
	Type      string
	// Platform is "windows", "linux", or "" when neither can be established. It answers the
	// question the picker could not previously answer — whether a host is worth pointing an
	// RDP client at — and it is deliberately not used to restrict the connection types on
	// offer: Windows Server ships an optional OpenSSH server, xrdp exists for Linux, and a
	// lab host may be running whatever someone put there.
	Platform string
	// PlatformDetails is the raw AWS string ("Windows", "Linux/UNIX", "Red Hat Enterprise
	// Linux", …). Kept for search rather than display: the longer values run to half a row,
	// but searching "red hat" is exactly the kind of narrowing the picker is for.
	PlatformDetails string
	// Tags is every tag on the instance, not just Name. DescribeInstances already returns
	// them all, so keeping them costs no extra call and no extra permission — and which tag
	// is the useful one to search varies by account (client, env, owner, scenario), so
	// picking a subset here would be guessing on the user's behalf.
	Tags map[string]string
}

// platformOf normalises what DescribeInstances reports into "windows", "linux", or "".
//
// The Platform field is the documented signal but a one-sided one: AWS sets it to "windows" for
// Windows instances and omits it for everything else, so absence means "not Windows" rather than
// "Linux". PlatformDetails is populated for billing on every instance and is what makes the
// positive Linux answer possible.
//
// Returning "" rather than guessing "linux" matters for the row: an empty badge reads as "warren
// does not know", whereas a wrong "linux" on a Windows host would send someone to ssh and waste
// the time this field exists to save.
func platformOf(platform, details string) string {
	switch {
	case strings.EqualFold(platform, "windows"):
		return "windows"
	case strings.Contains(strings.ToLower(details), "windows"):
		// Belt and braces: covers a response carrying details but no Platform field.
		return "windows"
	case details != "":
		return "linux"
	default:
		return ""
	}
}

// PlatformBadge is the fixed-width column shown at the front of an instance row. Padding to a
// common width keeps the id, IP, and type aligned down the list, which is what makes the rows
// scannable; an unpadded badge would ragged-edge every column after it.
func (i Instance) PlatformBadge() string {
	label := i.Platform
	if label == "" {
		label = "unknown"
	}
	return fmt.Sprintf("%-9s", "["+label+"]")
}

// TagPairs returns the tags as "key=value" strings, sorted so the result is stable enough
// to test and to render. Used to fold tags into the picker's search text.
func (i Instance) TagPairs() []string {
	pairs := make([]string, 0, len(i.Tags))
	for k, v := range i.Tags {
		pairs = append(pairs, k+"="+v)
	}
	sort.Strings(pairs)
	return pairs
}

// ListInstances returns all running EC2 instances using the given session creds.
func ListInstances(ctx context.Context, sess *Session) ([]Instance, error) {
	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(sess.Region),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			sess.AccessKeyID, sess.SecretAccessKey, sess.SessionToken,
		)),
	)
	if err != nil {
		return nil, err
	}
	client := ec2.NewFromConfig(cfg)

	// DescribeInstances caps a page at 1000 instances and returns a NextToken for the rest.
	// Ignoring it silently truncated the list — the picker looked complete while missing
	// hosts. The paginator is used rather than a hand-rolled NextToken loop (as in
	// ListAccounts) because EC2 signals "done" with an empty string as well as nil, which a
	// naive `!= nil` check spins on.
	paginator := ec2.NewDescribeInstancesPaginator(client, &ec2.DescribeInstancesInput{
		Filters: []types.Filter{
			{Name: aws.String("instance-state-name"), Values: []string{"running"}},
		},
	})

	var instances []Instance
	for paginator.HasMorePages() {
		out, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("describe instances: %w", err)
		}
		for _, r := range out.Reservations {
			for _, i := range r.Instances {
				inst := Instance{
					ID:              aws.ToString(i.InstanceId),
					PrivateIP:       aws.ToString(i.PrivateIpAddress),
					Type:            string(i.InstanceType),
					Name:            "(no name)",
					PlatformDetails: aws.ToString(i.PlatformDetails),
					Platform:        platformOf(string(i.Platform), aws.ToString(i.PlatformDetails)),
					Tags:            make(map[string]string, len(i.Tags)),
				}
				for _, tag := range i.Tags {
					k, v := aws.ToString(tag.Key), aws.ToString(tag.Value)
					if k == "" {
						continue
					}
					inst.Tags[k] = v
					// An empty Name tag is no better than no Name tag; keep the placeholder
					// so the row does not render as a blank title.
					if k == "Name" && v != "" {
						inst.Name = v
					}
				}
				instances = append(instances, inst)
			}
		}
	}
	sort.Slice(instances, func(i, j int) bool {
		return instances[i].Name < instances[j].Name
	})
	return instances, nil
}
