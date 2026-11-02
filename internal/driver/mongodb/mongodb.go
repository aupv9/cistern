// Package mongodb là driver "mongodb". FR-05: dump kèm oplog cho replica set.
package mongodb

import "cistern/internal/driver"

func init() {
	driver.Register("mongodb", func(p driver.Params) (driver.Driver, error) {
		// TODO: hiện thực driver thật.
		return driver.NewStub("mongodb", p), nil
	})
}
