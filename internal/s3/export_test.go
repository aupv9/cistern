package s3

import "time"

// SetBackoff rút ngắn thời gian chờ giữa các lần thử lại trong test.
func SetBackoff(c *Client, f func(int) time.Duration) { c.backoff = f }
