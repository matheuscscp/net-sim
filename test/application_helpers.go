package test

import (
	"context"
	"net/http"
	"time"
)

func HTTPGet(client *http.Client, url string) (*http.Response, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	return client.Do(req.WithContext(ctx))
}
