package nats

import "cistern/internal/driver"

func init() {
	driver.Register("nats", func(p driver.Params) (driver.Driver, error) {

		return driver.NewStub("nats", p), nil
	})
}
