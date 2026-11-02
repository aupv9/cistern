// Package s3 là driver "s3". FR-12: sao chép bucket/prefix, incremental theo ETag (index.json).
package s3

import "cistern/internal/driver"

func init() {
	driver.Register("s3", func(p driver.Params) (driver.Driver, error) {
		// TODO: hiện thực driver thật.
		return driver.NewStub("s3", p), nil
	})
}
