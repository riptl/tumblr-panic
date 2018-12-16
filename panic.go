package main

import (
	"fmt"
	"github.com/cenkalti/backoff"
	"github.com/spf13/pflag"
	"github.com/valyala/fastjson"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sync"
)

type job struct {
	blogName string
	url_     string
}

var cdn = make(chan job)
var wg sync.WaitGroup

var conns = pflag.Int("conns", 4, "Connections for media downloads")
var apiKey = pflag.String("api-key", "", "API Key")
var noMedia = pflag.Bool("no-media", false, "Don't save media")
var globalMedia = pflag.Bool("global-media", false, "Save all media in the same dir")
var noReblogs = pflag.Bool("no-reblogs", false, "Don't save media of reblogs")
var likes = pflag.Bool("likes", false, "Save likes instead of posts")

func main() {
	if *noMedia {
		*conns = 0
	} else if *globalMedia {
		os.MkdirAll("media", 0777)
	}

	wg.Add(*conns)
	for i := 0; i < *conns; i++ {
		go downloadWorker()
	}

	pflag.Parse()

	defer wg.Wait()
	defer close(cdn)

	for _, blogName := range pflag.Args() {
		println("################")
		println("## NEXT  BLOG ##")
		println("################")
		println()
		println(blogName)
		println()
		println()
		getBlog(blogName)
	}
}

func downloadWorker() {
	defer wg.Done()
	for job := range cdn {
		downloadFile(job.blogName, job.url_)
	}
}

func getBlog(blogUrl string) {
	os.MkdirAll(blogUrl, 0777)

	var hasMore bool = true
	for offset := 0; hasMore; offset += 20 {
		var err error
		_, hasMore, err = reqMetadata(blogUrl, offset)
		if err != nil {
			break
		}
	}
}

func reqMetadata(blogUrl string, offset int) (body []byte, hasMore bool, err error) {
	var url_ *url.URL
	var res *http.Response

	err = backoff.Retry(func() error {
		var action string
		if *likes {
			action = "likes"
		} else {
			action = "posts"
		}

		url_, _ = url.Parse(fmt.Sprintf("https://api-http2.tumblr.com/v2/blog/%s.tumblr.com/%s",
			blogUrl, action))

		url_.RawQuery = url.Values{
			"api_key":     {*apiKey},
			"limit":       {"20"},
			"offset":      {fmt.Sprintf("%d", offset)},
			"reblog_info": {"true"},
		}.Encode()

		req, _ := http.NewRequest("GET", url_.String(), nil)
		req.Header.Set("x-identifier-date", "1496497075")
		req.Header.Set("accept", "*/*")
		req.Header.Set("x-s-id-enabled", "true")
		req.Header.Set("authorization", `OAuth oauth_signature="5X71iWQbsn%2FXSxSW5yM1Gfba9AY%3D",oauth_nonce="242E14AF-C965-40E1-8374-C4EEE2CC7DB6",oauth_timestamp="1544897447.000000",oauth_consumer_key="jrsCWX1XDuVxAFO4GkK147syAoN8BJZ5voz8tS80bPcj26Vc5Z",oauth_token="bzDmGY6IYfwE43WBj75Hz7ZDiBIzW1jfPYOHvYPu4AEyOtM0O6",oauth_version="1.0",oauth_signature_method="HMAC-SHA1"`)
		req.Header.Set("yx", "1m6phjof3ocve")
		req.Header.Set("x-yuser-agent", "YMobile/1.0 (com.tumblr.tumblr/11.7.1; iOS/11.3.1;; iPhone8,1; Apple;;; 1334x750;)")
		req.Header.Set("x-version", "iPhone/11.7.1/117100/11.3.1/tumblr")
		req.Header.Set("accept-language", "de-DE")
		req.Header.Set("x-s-id", "Q0NBNUZEQzEtNUY3NC00QUFCLTlGQjMtMjY0OUNEMTNGN0VB")
		req.Header.Set("di", "DI/1.0 (262; 02; [WIFI])")
		//req.Header.Set("user-agent", "Tumblr/iPhone/11.7.1/117100/11.3.1/tumblr")
		req.Header.Set("user-agent", "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)")
		req.Header.Set("x-identifier", "16F9B3AC-8BEC-4D12-BFEE-E36AF38C2E13-495-0000003DA3273F80")
		req.Header.Set("cookie", "tmgioct=5954158de4caf10117554050")

		res, err = http.DefaultClient.Do(req)
		if err != nil {
			println("Failed getting", url_.String(), err)
			return err
		}

		if res.StatusCode != 200 {
			println("Failed getting ", url_.String(), res.Status)
			return fmt.Errorf("HTTP %s", res.Status)
		}

		return nil
	}, backoff.NewExponentialBackOff())
	if err != nil {
		println("Failed getting", err)
		return nil, false, err
	}

	println(url_.String())

	var jsonFile string
	if *likes {
		jsonFile = fmt.Sprintf("likes-%d.json", offset)
	} else {
		jsonFile = fmt.Sprintf("%d.json", offset)
	}
	f, err := os.OpenFile(filepath.Join(blogUrl, jsonFile), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0666)
	if err != nil {
		println("shid", err.Error())
		return nil, false, err
	}
	defer f.Close()

	body, err = ioutil.ReadAll(io.TeeReader(res.Body, f))
	if err != nil {
		println("Failed getting", url_.String(), err.Error())
		return nil, false, err
	}

	var p fastjson.Parser

	js, err := p.ParseBytes(body)
	if err != nil {
		println("Failed getting", url_.String(), err.Error())
		return nil, false, err
	}

	// Unclean AF
	var posts []*fastjson.Value
	if *likes {
		posts = js.GetArray("response", "liked_posts")
	} else {
		posts = js.GetArray("response", "posts")
	}
	if !*noMedia {
		if !*globalMedia {
			os.MkdirAll(filepath.Join(blogUrl, "media"), 0777)
		}

		for _, post := range posts {
			if *noReblogs && post.Exists("reblogged_from_id") {
				continue
			}

			postType := string(post.GetStringBytes("type"))
			switch postType {
			case "photo":
				for _, photo := range post.GetArray("photos") {
					url_ := string(photo.GetStringBytes("original_size", "url"))
					if url_ == "" {
						continue
					}
					cdn <- job{blogUrl, url_}
				}
			case "video":
				videoType := string(post.GetStringBytes("video_type"))
				if videoType == "tumblr" {
					videoUrl := string(post.GetStringBytes("video_url"))
					cdn <- job{blogUrl, videoUrl}
				}
			case "audio":
				audioUrl := string(post.GetStringBytes("audio_url"))
				if audioUrl == "" {
					audioUrl = string(post.GetStringBytes("audio_source_url"))
				}
				cdn <- job{blogUrl, audioUrl}
			}
		}
	}

	hasMore = len(posts) != 0

	return body, hasMore, nil
}

func downloadFile(blogName string, url_ string) {
	baseName := path.Base(url_)
	var fName string
	if *globalMedia {
		fName = filepath.Join("media", baseName)
	} else {
		fName = filepath.Join(blogName, "media", baseName)
	}

	res, err := http.Get(url_)
	if err != nil {
		println(err)
		return
	}
	defer res.Body.Close()

	if res.StatusCode != 200 {
		println("STATUS CODE", res.Status, url_)
		return
	}

	f, err := os.OpenFile(fName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0666)
	if os.IsExist(err) {
		return
	} else if err != nil {
		println(err)
		return
	}

	println(url_)

	_, err = io.Copy(f, res.Body)
	if err != nil {
		println(err)
		return
	}

	return
}
