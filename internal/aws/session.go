package aws

import "fmt"

// Creds returns raw credential fields for direct SDK use.
func (s *Session) Creds() (accessKey, secretKey, sessionToken, region string) {
	return s.AccessKeyID, s.SecretAccessKey, s.SessionToken, s.Region
}

// Env returns the session as AWS environment variable strings for os/exec.
func (s *Session) Env() []string {
	return []string{
		"AWS_ACCESS_KEY_ID=" + s.AccessKeyID,
		"AWS_SECRET_ACCESS_KEY=" + s.SecretAccessKey,
		"AWS_SESSION_TOKEN=" + s.SessionToken,
		"AWS_DEFAULT_REGION=" + s.Region,
		"AWS_REGION=" + s.Region,
	}
}

// BuildLabel sets the display label from resolved account/role info.
func (s *Session) BuildLabel(acctName, role string) {
	s.AccountName = acctName
	s.RoleName = role
	s.Label = fmt.Sprintf("%s (%s)/%s", acctName, s.AccountID, role)
}
