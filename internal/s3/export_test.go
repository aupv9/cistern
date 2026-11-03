package s3

import "time"

func SetBackoff(c *Client, f func(int) time.Duration) { c.backoff = f }
