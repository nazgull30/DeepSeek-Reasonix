package codegraph

// releaseAssetSHA256 holds the upstream SHA256SUMS values for Version. These
// are baked into the reasonix binary so downloaded CodeGraph archives are
// verified against trusted metadata instead of trusting whichever mirror served
// the bytes.
var releaseAssetSHA256 = map[string]string{
	"codegraph-darwin-arm64.tar.gz": "95bb27bf6382b69659e158e0c04d71cc394778951e1317d582be7807e7866908",
	"codegraph-darwin-x64.tar.gz":   "3311cc1d1f0f0ad742709b6a43d8a9187b1ef0af0dd30e0b58008dc673e29478",
	"codegraph-linux-arm64.tar.gz":  "e16f612bc96c2ebccd04574cbed500c9939147c80666ad6bb024398dff7992ae",
	"codegraph-linux-x64.tar.gz":    "d45a068f44596a85c7ba7d0ef924eaf7103fbbf3cafbeb668127daff60a52228",
	"codegraph-win32-arm64.zip":     "32190f7db56442b3663f1a4cac12c4cbc0de9a00bc12e6455365a217ed769aa5",
	"codegraph-win32-x64.zip":       "21c0e498c07eb17b4e90e7c4f2bd86d197ac0e1a103d98e77f1858c5b15d7e31",
}
