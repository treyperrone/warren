package aws

import (
	"context"
	"fmt"
	"sort"

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
					ID:        aws.ToString(i.InstanceId),
					PrivateIP: aws.ToString(i.PrivateIpAddress),
					Type:      string(i.InstanceType),
					Name:      "(no name)",
				}
				for _, tag := range i.Tags {
					if aws.ToString(tag.Key) == "Name" {
						inst.Name = aws.ToString(tag.Value)
						break
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
