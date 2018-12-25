package main

import (
	"fmt"
	"github.com/cenkalti/backoff"
	"github.com/spf13/pflag"
	"github.com/valyala/fastjson"
	"io"
	"io/ioutil"
	"log"
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
		if err := os.MkdirAll("media", 0777); err != nil {
			log.Fatal(err)
		}
	}

	wg.Add(*conns)
	for i := 0; i < *conns; i++ {
		go downloadWorker()
	}

	pflag.Parse()

	defer wg.Wait()
	defer close(cdn)

	for _, blogName := range pflag.Args() {
		log.Printf(`✳️ Starting archiver on "%s"`, blogName)
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
	if err := os.MkdirAll(blogUrl, 0777); err != nil {
		log.Printf(`❌ Failed to create blog directory: %s`, err)
		return
	}

	hasMore := true
	for offset := 0; hasMore; offset += 20 {
		var err error
		_, hasMore, err = reqMetadata(blogUrl, offset)
		if err != nil {
			log.Printf(`❌ Aborting at page %d of "%s": %s`, offset, blogUrl, err)
			break
		}
	}
}

func reqMetadata(blogUrl string, offset int) (body []byte, hasMore bool, err error) {
	var u *url.URL
	var res *http.Response

	err = backoff.Retry(func() error {
		var action string
		if *likes {
			action = "likes"
		} else {
			action = "posts"
		}

		u, _ = url.Parse(fmt.Sprintf("https://api-http2.tumblr.com/v2/blog/%s.tumblr.com/%s",
			blogUrl, action))

		u.RawQuery = url.Values{
			"api_key":     {*apiKey},
			"limit":       {"20"},
			"offset":      {fmt.Sprintf("%d", offset)},
			"reblog_info": {"true"},
		}.Encode()

		req, _ := http.NewRequest("GET", u.String(), nil)
		req.Header.Set("accept", "*/*")
		req.Header.Set("x-s-id-enabled", "true")
		req.Header.Set("x-yuser-agent", "YMobile/1.0 (com.tumblr.tumblr/11.7.1; iOS/11.3.1;; iPhone8,1; Apple;;; 1334x750;)")
		req.Header.Set("x-version", "iPhone/11.7.1/117100/11.3.1/tumblr")
		req.Header.Set("di", "DI/1.0 (262; 02; [WIFI])")
		//req.Header.Set("user-agent", "Tumblr/iPhone/11.7.1/117100/11.3.1/tumblr")
		req.Header.Set("user-agent", "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)")

		res, err = http.DefaultClient.Do(req)
		if err != nil {
			log.Printf(`❌️ Failed to get metadata ("%s"): %s`, u.String(), err)
			return err
		}

		if res.StatusCode != 200 {
			log.Printf(`❌️ Failed to get metadata ("%s"), non-OK HTTP status: %s`, u.String(), res.Status)
			return fmt.Errorf("HTTP %s", res.Status)
		}

		return nil
	}, backoff.NewExponentialBackOff())
	if err != nil {
		log.Printf(`⚠️ All attempts to get metadata ("%s") failed`, u.String())
		return nil, false, err
	}

	log.Printf(`ℹ️ Requested "%s"`, u.String())

	var jsonFile string
	if *likes {
		jsonFile = fmt.Sprintf("likes-%d.json", offset)
	} else {
		jsonFile = fmt.Sprintf("%d.json", offset)
	}
	f, err := os.OpenFile(filepath.Join(blogUrl, jsonFile), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0666)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()

	body, err = ioutil.ReadAll(io.TeeReader(res.Body, f))
	if err != nil {
		log.Printf(`❌ Failed to download metadata ("%s"): %s`, u.String(), err)
		return nil, false, err
	}

	var p fastjson.Parser

	js, err := p.ParseBytes(body)
	if err != nil {
		log.Printf(`❌ Failed to parse metadata ("%s"): %s`, u.String(), err)
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
			if err := os.MkdirAll(filepath.Join(blogUrl, "media"), 0777); err != nil {
				return nil, false, err
			}
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
		log.Printf(`❌ Failed to request media ("%s"): %s`, url_, err)
		return
	}
	defer res.Body.Close()

	if res.StatusCode != 200 {
		log.Printf(`❌️ Failed to get metadata ("%s"), non-OK HTTP status: %s`, url_, res.Status)
		return
	}

	f, err := os.OpenFile(fName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0666)
	if os.IsExist(err) {
		return
	} else if err != nil {
		log.Printf(`❌ Failed to download media ("%s"): %s`, url_, err)
		return
	}
	defer f.Close()

	_, err = io.Copy(f, res.Body)
	if err != nil {
		log.Printf(`❌ Failed to download media ("%s"): %s`, url_, err)
		// Try to delete failed file
		f.Close()
		os.Remove(fName)
		return
	}

	log.Printf(`🆗 Got media: %s`, url_)

	return
}
