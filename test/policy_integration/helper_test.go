//go:build integration

package policy_integration

import (
	"time"
)

const (
	TestTimeoutMedium       = time.Second * 10
	TestTimeoutLong         = time.Second * 30
	TestRetryIntervalMedium = time.Millisecond * 250
)
