package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"mime"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/volcengine/ve-tos-golang-sdk/v2/tos"
	"github.com/volcengine/ve-tos-golang-sdk/v2/tos/enum"
)

func readEnvFile(path string) map[string]string {
	values := map[string]string{}
	file, err := os.Open(path)
	if err != nil {
		return values
	}
	defer func() { _ = file.Close() }()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || !strings.Contains(line, "=") {
			continue
		}
		key, value, _ := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if key != "" {
			values[key] = value
		}
	}
	return values
}

func envValue(values map[string]string, key string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return values[key]
}

func contentTypeFor(path string) string {
	if contentType := mime.TypeByExtension(filepath.Ext(path)); contentType != "" {
		return contentType
	}
	return "application/octet-stream"
}

func proxyOptionFromEnv() tos.ClientOption {
	raw := envValue(map[string]string{}, "HTTPS_PROXY")
	if raw == "" {
		raw = envValue(map[string]string{}, "https_proxy")
	}
	if raw == "" {
		raw = envValue(map[string]string{}, "HTTP_PROXY")
	}
	if raw == "" {
		raw = envValue(map[string]string{}, "http_proxy")
	}
	if raw == "" {
		return nil
	}
	proxyURL, err := url.Parse(raw)
	if err != nil || proxyURL.Hostname() == "" {
		return nil
	}
	port := 80
	if value := proxyURL.Port(); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil {
			port = parsed
		}
	}
	scheme := proxyURL.Scheme
	if scheme == "" {
		scheme = "http"
	}
	proxy, err := tos.NewProxy(scheme+"://"+proxyURL.Hostname(), port)
	if err != nil {
		return nil
	}
	return tos.WithProxy(proxy)
}

func main() {
	var envPath string
	var dir string
	var prefix string
	var dryRun bool
	flag.StringVar(&envPath, "env", "deploy/.env", "path to env file with AUCTION_TOS_* values")
	flag.StringVar(&dir, "dir", "", "local directory to upload")
	flag.StringVar(&prefix, "prefix", "", "TOS object key prefix")
	flag.BoolVar(&dryRun, "dry-run", false, "print planned uploads without writing to TOS")
	flag.Parse()

	if dir == "" {
		fmt.Fprintln(os.Stderr, "--dir is required")
		os.Exit(2)
	}

	env := readEnvFile(envPath)
	endpoint := envValue(env, "AUCTION_TOS_ENDPOINT")
	region := envValue(env, "AUCTION_TOS_REGION")
	bucket := envValue(env, "AUCTION_TOS_BUCKET")
	accessKey := envValue(env, "AUCTION_TOS_ACCESS_KEY")
	secretKey := envValue(env, "AUCTION_TOS_SECRET_KEY")
	publicBaseURL := strings.TrimRight(envValue(env, "AUCTION_TOS_PUBLIC_BASE_URL"), "/")
	if endpoint == "" || region == "" || bucket == "" || accessKey == "" || secretKey == "" {
		fmt.Fprintln(os.Stderr, "missing AUCTION_TOS_ENDPOINT / REGION / BUCKET / ACCESS_KEY / SECRET_KEY")
		os.Exit(2)
	}

	prefix = strings.Trim(prefix, "/")
	options := []tos.ClientOption{
		tos.WithRegion(region),
		tos.WithCredentials(tos.NewStaticCredentials(accessKey, secretKey)),
	}
	if proxyOption := proxyOptionFromEnv(); proxyOption != nil {
		options = append(options, proxyOption)
	}
	client, err := tos.NewClientV2(endpoint, options...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "create tos client: %v\n", err)
		os.Exit(1)
	}

	var files []string
	if err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		files = append(files, path)
		return nil
	}); err != nil {
		fmt.Fprintf(os.Stderr, "walk dir: %v\n", err)
		os.Exit(1)
	}
	sort.Strings(files)

	uploaded := 0
	for _, localPath := range files {
		rel, err := filepath.Rel(dir, localPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "relative path %s: %v\n", localPath, err)
			os.Exit(1)
		}
		key := filepath.ToSlash(rel)
		if prefix != "" {
			key = prefix + "/" + key
		}
		info, err := os.Stat(localPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "stat %s: %v\n", localPath, err)
			os.Exit(1)
		}
		if dryRun {
			fmt.Printf("dry-run %s -> %s\n", localPath, key)
			continue
		}
		file, err := os.Open(localPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "open %s: %v\n", localPath, err)
			os.Exit(1)
		}
		_, err = client.PutObjectV2(context.Background(), &tos.PutObjectV2Input{
			PutObjectBasicInput: tos.PutObjectBasicInput{
				Bucket:        bucket,
				Key:           key,
				ContentLength: info.Size(),
				ContentType:   contentTypeFor(localPath),
				ACL:           enum.ACLPublicRead,
			},
			Content: file,
		})
		_ = file.Close()
		if err != nil {
			fmt.Fprintf(os.Stderr, "upload %s: %v\n", localPath, err)
			os.Exit(1)
		}
		uploaded += 1
	}

	fmt.Printf("uploaded %d files to tos://%s/%s\n", uploaded, bucket, prefix)
	if publicBaseURL != "" {
		fmt.Printf("public prefix: %s/%s\n", publicBaseURL, prefix)
	}
}
