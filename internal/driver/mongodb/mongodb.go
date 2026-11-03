package mongodb

import "cistern/internal/driver"

func init() {
	driver.Register("mongodb", func(p driver.Params) (driver.Driver, error) {

		return driver.NewStub("mongodb", p), nil
	})
}
