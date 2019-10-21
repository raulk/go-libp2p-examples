If you're using go1.13+, make sure to bypass the go module proxy.

```
env GOPRIVATE='github.com/libp2p/*' go get github.com/libp2p/go-libp2p
```