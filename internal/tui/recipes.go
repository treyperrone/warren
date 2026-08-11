package tui

import "fmt"

// A recipe is one AWS CLI command with fill-in-the-blank parameters.
//
// Read-only by design — describe, list, get. A guided builder that can hand someone a working
// `terminate-instances` is a footgun, and the audience for a builder is exactly the person
// least equipped to spot one. Anything that changes state stays in the shell, where you had to
// type it out yourself.
//
// The list is deliberately a starter set rather than an attempt at coverage: AWS has hundreds
// of services and chasing them would be a treadmill that is always one release behind what you
// need. What stops that being a limitation is that the built command is editable before it
// runs — if your task is not here, start from the nearest one and change it. Every command is
// shown in full for the same reason: the goal is for you to stop needing the menu.
type recipe struct {
	title string
	desc  string
	// params are prompted in order; build receives their values in the same order.
	params []param
	build  func(v []string) string
}

// param is one input on the recipe's form.
type param struct {
	label       string
	placeholder string
	// def is prefilled and editable, for values that have a sensible common answer. Unlike a
	// placeholder it counts as an answer, so enter accepts it.
	def string
}

// service groups recipes as the CLI itself does, so what you learn here transfers.
type service struct {
	name    string
	desc    string
	recipes []recipe
}

// arg returns v[i], or "" when the slice is short — a recipe must never index past its own
// parameter list just because a form was rendered before every field existed.
func arg(v []string, i int) string {
	if i < len(v) {
		return v[i]
	}
	return ""
}

// wildcard wraps a value in * for a substring match, or matches anything when left blank.
// Partial tag matching is the whole point: "web" should find "globogym-web-01".
func wildcard(s string) string {
	if s == "" {
		return "*"
	}
	return "*" + s + "*"
}

var services = []service{
	{
		name: "EC2",
		desc: "instances, security groups, volumes, snapshots",
		recipes: []recipe{
			{
				title: "list instances by tag",
				desc:  "partial match on any tag — the usual way to find a host",
				params: []param{
					{label: "tag key", placeholder: "Name", def: "Name"},
					{label: "tag value contains", placeholder: "web"},
				},
				build: func(v []string) string {
					return fmt.Sprintf(
						`aws ec2 describe-instances --filters "Name=tag:%s,Values=%s" `+
							`--query "Reservations[].Instances[].[InstanceId,Tags[?Key=='Name'].Value|[0],PrivateIpAddress,InstanceType,State.Name]" `+
							`--output table`,
						arg(v, 0), wildcard(arg(v, 1)))
				},
			},
			{
				title: "describe one instance",
				desc:  "everything AWS knows about a single instance",
				params: []param{
					{label: "instance ID", placeholder: "i-0abc123def456"},
				},
				build: func(v []string) string {
					return fmt.Sprintf("aws ec2 describe-instances --instance-ids %s --output json", arg(v, 0))
				},
			},
			{
				title: "list security groups",
				desc:  "group IDs, names, and descriptions",
				build: func([]string) string {
					return `aws ec2 describe-security-groups ` +
						`--query "SecurityGroups[].[GroupId,GroupName,VpcId,Description]" --output table`
				},
			},
			{
				title: "list volumes",
				desc:  "size, state, and what each is attached to",
				build: func([]string) string {
					return `aws ec2 describe-volumes ` +
						`--query "Volumes[].[VolumeId,Size,State,Attachments[0].InstanceId]" --output table`
				},
			},
			{
				title: "list snapshots",
				desc:  "snapshots this account owns",
				build: func([]string) string {
					return `aws ec2 describe-snapshots --owner-ids self ` +
						`--query "Snapshots[].[SnapshotId,VolumeId,StartTime,Description]" --output table`
				},
			},
			{
				title: "get console output",
				desc:  "boot log — the first thing to read when SSM will not connect",
				params: []param{
					{label: "instance ID", placeholder: "i-0abc123def456"},
				},
				build: func(v []string) string {
					return fmt.Sprintf("aws ec2 get-console-output --instance-id %s --output text", arg(v, 0))
				},
			},
		},
	},
	{
		name: "S3",
		desc: "buckets, objects, policies",
		recipes: []recipe{
			{
				title: "list buckets",
				desc:  "every bucket in the account",
				build: func([]string) string {
					return `aws s3api list-buckets --query "Buckets[].[Name,CreationDate]" --output table`
				},
			},
			{
				title: "list objects in a bucket",
				desc:  "with sizes and a total",
				params: []param{
					{label: "bucket", placeholder: "my-bucket"},
					{label: "prefix (optional)", placeholder: "logs/"},
				},
				build: func(v []string) string {
					return fmt.Sprintf("aws s3 ls s3://%s/%s --human-readable --summarize",
						arg(v, 0), arg(v, 1))
				},
			},
			{
				title: "get bucket policy",
				desc:  "who else can reach it",
				params: []param{
					{label: "bucket", placeholder: "my-bucket"},
				},
				build: func(v []string) string {
					return fmt.Sprintf("aws s3api get-bucket-policy --bucket %s --output text", arg(v, 0))
				},
			},
			{
				title: "get bucket encryption",
				desc:  "default encryption settings",
				params: []param{
					{label: "bucket", placeholder: "my-bucket"},
				},
				build: func(v []string) string {
					return fmt.Sprintf("aws s3api get-bucket-encryption --bucket %s --output json", arg(v, 0))
				},
			},
		},
	},
	{
		name: "IAM",
		desc: "who you are, roles, attached policies",
		recipes: []recipe{
			{
				title: "who am I",
				desc:  "the identity these credentials actually resolve to",
				build: func([]string) string {
					return "aws sts get-caller-identity --output table"
				},
			},
			{
				title: "list roles",
				desc:  "every role in the account",
				build: func([]string) string {
					return `aws iam list-roles --query "Roles[].[RoleName,Arn]" --output table`
				},
			},
			{
				title: "list a role's attached policies",
				desc:  "managed policies on one role",
				params: []param{
					{label: "role name", placeholder: "CyberRangeAdmin"},
				},
				build: func(v []string) string {
					return fmt.Sprintf("aws iam list-attached-role-policies --role-name %s --output table", arg(v, 0))
				},
			},
			{
				title: "list a role's inline policies",
				desc:  "the ones that are easy to forget",
				params: []param{
					{label: "role name", placeholder: "CyberRangeAdmin"},
				},
				build: func(v []string) string {
					return fmt.Sprintf("aws iam list-role-policies --role-name %s --output table", arg(v, 0))
				},
			},
		},
	},
	{
		name: "SSM",
		desc: "which hosts are reachable, and command history",
		recipes: []recipe{
			{
				title: "list managed instances",
				desc:  "which hosts SSM can actually reach — start here when one is missing",
				build: func([]string) string {
					return `aws ssm describe-instance-information ` +
						`--query "InstanceInformationList[].[InstanceId,ComputerName,PingStatus,PlatformName,AgentVersion]" ` +
						`--output table`
				},
			},
			{
				title: "recent run-command history",
				desc:  "what has been run through SSM lately",
				build: func([]string) string {
					return `aws ssm list-commands --max-items 20 ` +
						`--query "Commands[].[CommandId,DocumentName,Status,RequestedDateTime]" --output table`
				},
			},
		},
	},
	{
		name: "CloudWatch Logs",
		desc: "log groups and recent log events",
		recipes: []recipe{
			{
				title: "list log groups",
				desc:  "names and stored size",
				build: func([]string) string {
					return `aws logs describe-log-groups --query "logGroups[].[logGroupName,storedBytes]" --output table`
				},
			},
			{
				title: "read a log group",
				desc:  "recent events; add --follow when editing to keep streaming",
				params: []param{
					{label: "log group", placeholder: "/aws/lambda/my-function"},
					{label: "since", placeholder: "10m", def: "10m"},
				},
				build: func(v []string) string {
					return fmt.Sprintf("aws logs tail %s --since %s", arg(v, 0), arg(v, 1))
				},
			},
		},
	},
}
