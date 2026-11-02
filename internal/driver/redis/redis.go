// Package redis là driver "redis". FR-06: snapshot RDB.
package redis

import "cistern/internal/driver"

func init() {
	driver.Register("redis", func(p driver.Params) (driver.Driver, error) {
		// TODO: hiện thực driver thật.
		return driver.NewStub("redis", p), nil
	})
}
