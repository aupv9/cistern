package repo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"cistern/internal/s3"
)

type S3Config struct {
	Endpoint, Region, Bucket string
	PathStyle                bool
	AccessKeyID              string
	SecretAccessKey          string
	SessionToken             string
	PartSizeMB               int
	CAFile                   string
	InsecureSkipVerify       bool
	LockMode                 string
	LockDays                 int
	Log                      *slog.Logger
}

type S3 struct {
	c        *s3.Client
	lockMode string
	lockDays int
	now      func() time.Time
}

type Checker interface {
	Check(ctx context.Context) error
}

func NewS3(cfg S3Config) (*S3, error) {
	cred := s3.Credentials{AccessKeyID: cfg.AccessKeyID, SecretAccessKey: cfg.SecretAccessKey, SessionToken: cfg.SessionToken}
	if cred.AccessKeyID == "" && cred.SecretAccessKey == "" {
		cred = s3.Credentials{
			AccessKeyID:     os.Getenv("AWS_ACCESS_KEY_ID"),
			SecretAccessKey: os.Getenv("AWS_SECRET_ACCESS_KEY"),
			SessionToken:    os.Getenv("AWS_SESSION_TOKEN"),
		}
	}
	if cred.AccessKeyID == "" || cred.SecretAccessKey == "" {
		return nil, errors.New("s3 repository: no credentials (set access_key_id/secret_access_key or AWS_ACCESS_KEY_ID/AWS_SECRET_ACCESS_KEY; IRSA and instance profiles are not supported yet)")
	}
	c, err := s3.New(s3.Config{
		Endpoint: cfg.Endpoint, Region: cfg.Region, Bucket: cfg.Bucket, PathStyle: cfg.PathStyle,
		Credentials: cred, PartSize: int64(cfg.PartSizeMB) << 20,
		CAFile: cfg.CAFile, InsecureSkipVerify: cfg.InsecureSkipVerify, Log: cfg.Log,
	})
	if err != nil {
		return nil, err
	}
	return &S3{c: c, lockMode: cfg.LockMode, lockDays: cfg.LockDays, now: time.Now}, nil
}

func (r *S3) Client() *s3.Client { return r.c }

func (r *S3) retention() *s3.Retention {
	if r.lockMode == "" {
		return nil
	}
	return &s3.Retention{Mode: r.lockMode, Until: r.now().Add(time.Duration(r.lockDays) * 24 * time.Hour).Truncate(time.Second)}
}

func (r *S3) Put(ctx context.Context, key string, rd io.Reader) error {
	if err := ValidKey(key); err != nil {
		return err
	}
	return r.c.Upload(ctx, key, ctxReader{ctx, rd}, s3.PutOptions{Retention: r.retention()})
}

func (r *S3) PutIfAbsent(ctx context.Context, key string, data []byte) error {
	if err := ValidKey(key); err != nil {
		return err
	}
	err := r.c.PutObject(ctx, key, data, s3.PutOptions{IfNoneMatch: true})
	if errors.Is(err, s3.ErrPreconditionFailed) {
		return fmt.Errorf("%s: %w", key, ErrExist)
	}
	return err
}

func (r *S3) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := ValidKey(key); err != nil {
		return nil, err
	}
	rc, err := r.c.GetObject(ctx, key)
	if errors.Is(err, s3.ErrNotFound) {
		return nil, fmt.Errorf("%s: %w", key, ErrNotExist)
	}
	return rc, err
}

func (r *S3) List(ctx context.Context, prefix string) ([]string, error) {
	return r.c.ListObjects(ctx, prefix)
}

func (r *S3) Delete(ctx context.Context, key string) error {
	if err := ValidKey(key); err != nil {
		return err
	}
	return r.c.DeleteObject(ctx, key)
}

func (r *S3) Check(ctx context.Context) error {
	if err := r.c.Probe(ctx, ""); err != nil {
		return err
	}
	if r.lockMode == "" {
		return nil
	}
	ok, err := r.c.ObjectLockEnabled(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("object_lock is configured but the bucket does not have Object Lock enabled (it can only be enabled when the bucket is created)")
	}
	return nil
}

type Retainer interface {
	LockMode() string
	PutIfAbsentUntil(ctx context.Context, key string, data []byte, until time.Time) error
}

func (r *S3) LockMode() string { return r.lockMode }

func (r *S3) PutIfAbsentUntil(ctx context.Context, key string, data []byte, until time.Time) error {
	if err := ValidKey(key); err != nil {
		return err
	}
	opts := s3.PutOptions{IfNoneMatch: true}
	if r.lockMode != "" {
		opts.Retention = &s3.Retention{Mode: r.lockMode, Until: until.UTC().Truncate(time.Second)}
	}
	err := r.c.PutObject(ctx, key, data, opts)
	if errors.Is(err, s3.ErrPreconditionFailed) {
		return fmt.Errorf("%s: %w", key, ErrExist)
	}
	return err
}
