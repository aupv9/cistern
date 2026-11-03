package s3

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestSigV4AWSVectors(t *testing.T) {
	cred := Credentials{AccessKeyID: "AKIAIOSFODNN7EXAMPLE", SecretAccessKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"}
	ts := time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name, method, url, payload, sig, signed string
		headers                                 map[string]string
	}{
		{"GET object", "GET", "https://examplebucket.s3.amazonaws.com/test.txt", EmptySHA256,
			"f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41",
			"host;range;x-amz-content-sha256;x-amz-date", map[string]string{"Range": "bytes=0-9"}},
		{"PUT object", "PUT", "https://examplebucket.s3.amazonaws.com/test$file.text",
			"44ce7dd67c959e0d3524ffac1771dfbba87d2b6b4b4e99e42034a8b803f8b072",
			"98ad721746da40c64f1a55b78f14c238d841ea1380cd77a1b5971af0ece108bd",
			"date;host;x-amz-content-sha256;x-amz-date;x-amz-storage-class",
			map[string]string{"Date": "Fri, 24 May 2013 00:00:00 GMT", "X-Amz-Storage-Class": "REDUCED_REDUNDANCY"}},
		{"GET lifecycle", "GET", "https://examplebucket.s3.amazonaws.com/?lifecycle", EmptySHA256,
			"fea454ca298b7da1c68078a5d1bdbfbbe0d65c699e0f91ac7a200a0136783543",
			"host;x-amz-content-sha256;x-amz-date", nil},
		{"list objects", "GET", "https://examplebucket.s3.amazonaws.com/?max-keys=2&prefix=J", EmptySHA256,
			"34b48302e7b5fa45bde8084f4b7868a86f0a534bc59db6670ed5711ef69dc6f7",
			"host;x-amz-content-sha256;x-amz-date", nil},
	}
	for _, c := range cases {
		req, _ := http.NewRequest(c.method, c.url, nil)
		for k, v := range c.headers {
			req.Header.Set(k, v)
		}
		Sign(req, cred, "us-east-1", c.payload, ts)
		auth := req.Header.Get("Authorization")
		if !strings.Contains(auth, "SignedHeaders="+c.signed+",") || !strings.HasSuffix(auth, "Signature="+c.sig) {
			t.Errorf("%s:\n got  %s\n want SignedHeaders=%s Signature=%s", c.name, auth, c.signed, c.sig)
		}
		if err := Verify(req, cred, "us-east-1"); err != nil {
			t.Errorf("%s: Verify: %v", c.name, err)
		}
		req.Header.Set("X-Amz-Date", "20130524T000001Z")
		if Verify(req, cred, "us-east-1") == nil {
			t.Errorf("%s: Verify accepted a modified request", c.name)
		}
	}
}

func TestURIEncode(t *testing.T) {
	if got := uriEncode("/a b/c+d=e@f~g", false); got != "/a%20b/c%2Bd%3De%40f~g" {
		t.Error(got)
	}
	if got := uriEncode("a/b", true); got != "a%2Fb" {
		t.Error(got)
	}
}
