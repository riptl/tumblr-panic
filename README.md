# tumblr-panic
Quick and dirty Tumblr archiver

Not the finest archiver, but it works
```
Usage of ./tumblr-panic:
      --api-key string   API Key
      --conns int        Connections for media downloads (default 4)
      --global-media     Save all media in the same dir
      --no-media         Don't save media
      --no-reblogs       Don't save media of reblogs
      <blog-name-1> <blog-name-2> ... <blog-name-n>
```

Media is saved in flat dir `/media`.

Blog JSON is saved in `/<blog_name>/<start_index>`.
