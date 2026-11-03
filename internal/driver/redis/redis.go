package redis

import "cistern/internal/driver"

func init() {
	driver.Register("redis", func(p driver.Params) (driver.Driver, error) {

		return driver.NewStub("redis", p), nil
	})
}
