package exec

import "cistern/internal/driver"

func init() {
	driver.Register("exec", func(p driver.Params) (driver.Driver, error) {

		return driver.NewStub("exec", p), nil
	})
}
