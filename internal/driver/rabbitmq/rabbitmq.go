package rabbitmq

import "cistern/internal/driver"

func init() {
	driver.Register("rabbitmq", func(p driver.Params) (driver.Driver, error) {

		return driver.NewStub("rabbitmq", p), nil
	})
}
