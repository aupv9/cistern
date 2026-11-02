// Package elasticsearch là driver "elasticsearch". FR-08: Snapshot API; cần hiện thực driver.Forgetter.
package elasticsearch

import "cistern/internal/driver"

func init() {
	driver.Register("elasticsearch", func(p driver.Params) (driver.Driver, error) {
		// TODO: hiện thực driver thật.
		return driver.NewStub("elasticsearch", p), nil
	})
}
