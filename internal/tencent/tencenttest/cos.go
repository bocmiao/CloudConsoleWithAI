package tencenttest

import (
	"bytes"
	"crypto/md5"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/tencent"
)

// COSAppID is the fake account's APPID, the suffix of its bucket names.
const COSAppID = "1250000000"

// COSBucket is a bucket held by the fake.
type COSBucket struct {
	Name, Region string
	Created      time.Time
	ACL          tencent.ACL
	Referer      *tencent.Referer
	CORS         []tencent.CORSRule
	Lifecycle    string // XML
	Versioning   string
	Encryption   string
	Website      *tencent.Website
	Policy       string
	Objects      map[string]*COSObject
}

// COSObject is a file held by the fake.
type COSObject struct {
	Data         []byte
	ContentType  string
	Modified     time.Time
	StorageClass string // STANDARD when empty
}

// AddBucket adds a bucket for a test or a demo.
func (f *Fake) AddBucket(name, region, acl string) *COSBucket {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.COS == nil {
		f.COS = map[string]*COSBucket{}
	}
	b := &COSBucket{Name: name, Region: region, Created: time.Now().Add(-240 * time.Hour), ACL: tencent.ACL{Canned: acl, Grants: map[string]string{}},
		Objects: map[string]*COSObject{}}
	f.COS[name] = b
	return b
}

// PutFile adds a file to a bucket.
func (b *COSBucket) PutFile(key string, data []byte) {
	b.Objects[key] = &COSObject{Data: data, ContentType: "application/octet-stream", Modified: time.Now().Add(-time.Hour)}
}

// Bucket returns a copy of a bucket, for assertions.
func (f *Fake) Bucket(name string) *COSBucket {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.COS[name]
	if !ok {
		return nil
	}
	c := *b
	c.Objects = map[string]*COSObject{}
	for k, v := range b.Objects {
		o := *v
		c.Objects[k] = &o
	}
	return &c
}

func cosFail(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	fmt.Fprintf(w, "<Error><Code>%s</Code><Message>%s</Message><RequestId>fake-req</RequestId></Error>", code, msg)
}

func cosXML(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/xml")
	data, _ := xml.Marshal(v)
	_, _ = w.Write(data)
}

// cosSigned checks a request's q-sign Authorization, from the query (a
// presigned link) or the header.
func cosSigned(r *http.Request, path string, body []byte) bool {
	auth := r.Header.Get("Authorization")
	q := r.URL.Query()
	if auth == "" && q.Get("q-signature") != "" {
		var parts []string
		for _, k := range []string{"q-sign-algorithm", "q-ak", "q-sign-time", "q-key-time", "q-header-list", "q-url-param-list", "q-signature"} {
			parts = append(parts, k+"="+q.Get(k))
			q.Del(k)
		}
		auth = strings.Join(parts, "&")
	}
	fields := map[string]string{}
	for _, p := range strings.Split(auth, "&") {
		k, v, _ := strings.Cut(p, "=")
		fields[k] = v
	}
	if fields["q-ak"] != SecretID {
		return false
	}
	st, en, _ := strings.Cut(fields["q-sign-time"], ";")
	s, _ := strconv.ParseInt(st, 10, 64)
	e, _ := strconv.ParseInt(en, 10, 64)
	now := time.Now().Unix()
	if now < s-5 || now > e {
		return false
	}
	h := http.Header{}
	for _, name := range strings.Split(fields["q-header-list"], ";") {
		if name == "" {
			continue
		}
		if name == "host" {
			h.Set("Host", r.Host)
		} else {
			h.Set(name, r.Header.Get(name))
		}
	}
	params := url.Values{}
	for _, name := range strings.Split(fields["q-url-param-list"], ";") {
		if name != "" {
			params[name] = q[name]
		}
	}
	// Every parameter sent must be signed.
	for k := range q {
		if _, ok := params[strings.ToLower(k)]; !ok {
			return false
		}
	}
	return tencent.COSAuthorization(SecretID, SecretKey, r.Method, path, params, h, time.Unix(s, 0), time.Unix(e, 0)) == auth
}

func (f *Fake) serveCOS(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/cos")
	if path == "" {
		path = "/"
	}
	body, _ := io.ReadAll(r.Body)
	if !cosSigned(r, path, body) {
		cosFail(w, http.StatusForbidden, "SignatureDoesNotMatch", "The Signature you specified is invalid.")
		return
	}
	if sum := r.Header.Get("Content-MD5"); sum != "" {
		want := md5.Sum(body)
		if sum != base64.StdEncoding.EncodeToString(want[:]) {
			cosFail(w, http.StatusBadRequest, "InvalidDigest", "The Content-MD5 you specified is not valid.")
			return
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls = append(f.Calls, "cos "+r.Method+" "+r.Host+path+"?"+r.URL.RawQuery)
	if f.DeniedService == "cos" {
		cosFail(w, http.StatusForbidden, "AccessDenied", "Access Denied.")
		return
	}
	if r.Host == "service.cos.myqcloud.com" {
		type bucket struct {
			Name     string `xml:"Name"`
			Location string `xml:"Location"`
			Created  string `xml:"CreationDate"`
		}
		var list []bucket
		for _, b := range f.COS {
			list = append(list, bucket{b.Name, b.Region, b.Created.UTC().Format(time.RFC3339)})
		}
		sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
		cosXML(w, struct {
			XMLName xml.Name `xml:"ListAllMyBucketsResult"`
			Owner   struct {
				ID string `xml:"ID"`
			} `xml:"Owner"`
			Buckets []bucket `xml:"Buckets>Bucket"`
		}{Buckets: list})
		return
	}
	name, rest, _ := strings.Cut(r.Host, ".cos.")
	region, _, _ := strings.Cut(rest, ".myqcloud.com")
	key := strings.TrimPrefix(path, "/")
	q := r.URL.Query()
	b := f.COS[name]
	if b == nil && !(r.Method == http.MethodPut && key == "" && len(q) == 0) {
		cosFail(w, http.StatusNotFound, "NoSuchBucket", "The specified bucket does not exist.")
		return
	}
	if b != nil && b.Region != region {
		cosFail(w, http.StatusNotFound, "NoSuchBucket", "The specified bucket is in another region.")
		return
	}
	has := func(k string) bool { _, ok := q[k]; return ok }
	switch {
	case key == "" && r.Method == http.MethodPut && len(q) == 0:
		if b != nil {
			cosFail(w, http.StatusConflict, "BucketAlreadyOwnedByYou", "The bucket already exists.")
			return
		}
		if !strings.HasSuffix(name, "-"+COSAppID) {
			cosFail(w, http.StatusBadRequest, "InvalidBucketName", "The bucket name is invalid.")
			return
		}
		acl := r.Header.Get("x-cos-acl")
		if acl == "" {
			acl = "private"
		}
		if f.COS == nil {
			f.COS = map[string]*COSBucket{}
		}
		f.COS[name] = &COSBucket{Name: name, Region: region, Created: time.Now(), ACL: tencent.ACL{Canned: acl, Grants: map[string]string{}}, Objects: map[string]*COSObject{}}
	case key == "" && r.Method == http.MethodDelete && len(q) == 0:
		if len(b.Objects) > 0 {
			cosFail(w, http.StatusConflict, "BucketNotEmpty", "The bucket you tried to delete is not empty.")
			return
		}
		delete(f.COS, name)
		w.WriteHeader(http.StatusNoContent)
	case key == "" && r.Method == http.MethodHead:
	case key == "" && has("acl"):
		f.cosACL(w, r, b)
	case key == "" && has("referer"):
		switch r.Method {
		case http.MethodGet:
			if b.Referer == nil {
				cosFail(w, http.StatusNotFound, "NoSuchRefererConfiguration", "The referer configuration does not exist.")
				return
			}
			cosXML(w, b.Referer)
		case http.MethodPut:
			var ref tencent.Referer
			if xml.Unmarshal(body, &ref) != nil || (ref.Status != "Enabled" && ref.Status != "Disabled") {
				cosFail(w, http.StatusBadRequest, "MalformedXML", "bad referer")
				return
			}
			b.Referer = &ref
		}
	case key == "" && has("cors"):
		switch r.Method {
		case http.MethodGet:
			if b.CORS == nil {
				cosFail(w, http.StatusNotFound, "NoSuchCORSConfiguration", "The CORS configuration does not exist.")
				return
			}
			cosXML(w, struct {
				XMLName xml.Name           `xml:"CORSConfiguration"`
				Rules   []tencent.CORSRule `xml:"CORSRule"`
			}{Rules: b.CORS})
		case http.MethodPut:
			if r.Header.Get("Content-MD5") == "" {
				cosFail(w, http.StatusBadRequest, "MissingContentMD5", "Content-MD5 is required.")
				return
			}
			var out struct {
				Rules []tencent.CORSRule `xml:"CORSRule"`
			}
			if xml.Unmarshal(body, &out) != nil || len(out.Rules) == 0 {
				cosFail(w, http.StatusBadRequest, "MalformedXML", "bad cors")
				return
			}
			b.CORS = out.Rules
		case http.MethodDelete:
			b.CORS = nil
			w.WriteHeader(http.StatusNoContent)
		}
	case key == "" && has("lifecycle"):
		switch r.Method {
		case http.MethodGet:
			if b.Lifecycle == "" {
				cosFail(w, http.StatusNotFound, "NoSuchLifecycleConfiguration", "The lifecycle configuration does not exist.")
				return
			}
			w.Header().Set("Content-Type", "application/xml")
			_, _ = io.WriteString(w, b.Lifecycle)
		case http.MethodPut:
			if r.Header.Get("Content-MD5") == "" {
				cosFail(w, http.StatusBadRequest, "MissingContentMD5", "Content-MD5 is required.")
				return
			}
			if rules, err := tencent.ParseLifecycle(string(body)); err != nil || len(rules) == 0 {
				cosFail(w, http.StatusBadRequest, "MalformedXML", "bad lifecycle")
				return
			}
			b.Lifecycle = string(body)
		case http.MethodDelete:
			b.Lifecycle = ""
			w.WriteHeader(http.StatusNoContent)
		}
	case key == "" && has("versioning"):
		switch r.Method {
		case http.MethodGet:
			cosXML(w, struct {
				XMLName xml.Name `xml:"VersioningConfiguration"`
				Status  string   `xml:"Status,omitempty"`
			}{Status: b.Versioning})
		case http.MethodPut:
			var v struct {
				Status string `xml:"Status"`
			}
			_ = xml.Unmarshal(body, &v)
			if v.Status != "Enabled" && v.Status != "Suspended" {
				cosFail(w, http.StatusBadRequest, "MalformedXML", "bad versioning")
				return
			}
			b.Versioning = v.Status
		}
	case key == "" && has("encryption"):
		switch r.Method {
		case http.MethodGet:
			if b.Encryption == "" {
				cosFail(w, http.StatusNotFound, "NoSuchEncryptionConfiguration", "The encryption configuration does not exist.")
				return
			}
			cosXML(w, struct {
				XMLName   xml.Name `xml:"ServerSideEncryptionConfiguration"`
				Algorithm string   `xml:"Rule>ApplyServerSideEncryptionByDefault>SSEAlgorithm"`
			}{Algorithm: b.Encryption})
		case http.MethodPut:
			var v struct {
				Algorithm string `xml:"Rule>ApplyServerSideEncryptionByDefault>SSEAlgorithm"`
			}
			_ = xml.Unmarshal(body, &v)
			b.Encryption = v.Algorithm
		case http.MethodDelete:
			b.Encryption = ""
			w.WriteHeader(http.StatusNoContent)
		}
	case key == "" && has("website"):
		switch r.Method {
		case http.MethodGet:
			if b.Website == nil {
				cosFail(w, http.StatusNotFound, "NoSuchWebsiteConfiguration", "The website configuration does not exist.")
				return
			}
			type redirect struct {
				Protocol string `xml:"Protocol"`
			}
			v := struct {
				XMLName  xml.Name  `xml:"WebsiteConfiguration"`
				Index    string    `xml:"IndexDocument>Suffix"`
				Redirect *redirect `xml:"RedirectAllRequestsTo,omitempty"`
				Error    string    `xml:"ErrorDocument>Key,omitempty"`
			}{Index: b.Website.Index, Error: b.Website.Error}
			if b.Website.HTTPS {
				v.Redirect = &redirect{"https"}
			}
			cosXML(w, v)
		case http.MethodPut:
			var v struct {
				Index    string `xml:"IndexDocument>Suffix"`
				Error    string `xml:"ErrorDocument>Key"`
				Protocol string `xml:"RedirectAllRequestsTo>Protocol"`
			}
			if xml.Unmarshal(body, &v) != nil || v.Index == "" {
				cosFail(w, http.StatusBadRequest, "MalformedXML", "bad website")
				return
			}
			b.Website = &tencent.Website{Enabled: true, Index: v.Index, Error: v.Error, HTTPS: v.Protocol == "https"}
		case http.MethodDelete:
			b.Website = nil
			w.WriteHeader(http.StatusNoContent)
		}
	case key == "" && has("policy"):
		switch r.Method {
		case http.MethodGet:
			if b.Policy == "" {
				cosFail(w, http.StatusNotFound, "NoSuchBucketPolicy", "The bucket policy does not exist.")
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, b.Policy)
		case http.MethodPut:
			if !json.Valid(body) {
				cosFail(w, http.StatusBadRequest, "MalformedPolicy", "The policy is not valid JSON.")
				return
			}
			b.Policy = string(body)
		case http.MethodDelete:
			b.Policy = ""
			w.WriteHeader(http.StatusNoContent)
		}
	case key == "" && has("delete") && r.Method == http.MethodPost:
		if r.Header.Get("Content-MD5") == "" {
			cosFail(w, http.StatusBadRequest, "MissingContentMD5", "Content-MD5 is required.")
			return
		}
		var req struct {
			Objects []struct {
				Key string `xml:"Key"`
			} `xml:"Object"`
		}
		_ = xml.Unmarshal(body, &req)
		for _, o := range req.Objects {
			delete(b.Objects, o.Key)
		}
		cosXML(w, struct {
			XMLName xml.Name `xml:"DeleteResult"`
		}{})
	case key == "" && r.Method == http.MethodGet:
		f.cosList(w, q, b)
	case key != "" && r.Method == http.MethodPut && r.Header.Get("x-cos-copy-source") != "":
		src := r.Header.Get("x-cos-copy-source")
		_, srcKey, _ := strings.Cut(src, ".myqcloud.com/")
		srcKey, _ = url.PathUnescape(srcKey)
		o := b.Objects[srcKey]
		if o == nil {
			cosFail(w, http.StatusNotFound, "NoSuchKey", "The specified key does not exist.")
			return
		}
		c := *o
		c.Modified = time.Now()
		b.Objects[key] = &c
		cosXML(w, struct {
			XMLName xml.Name `xml:"CopyObjectResult"`
		}{})
	case key != "" && r.Method == http.MethodPut:
		if int64(len(body)) != r.ContentLength {
			cosFail(w, http.StatusBadRequest, "IncompleteBody", "body shorter than Content-Length")
			return
		}
		b.Objects[key] = &COSObject{Data: body, ContentType: r.Header.Get("Content-Type"), Modified: time.Now()}
	case key != "" && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		o := b.Objects[key]
		if o == nil {
			if r.Method == http.MethodHead {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			cosFail(w, http.StatusNotFound, "NoSuchKey", "The specified key does not exist.")
			return
		}
		if o.ContentType != "" {
			w.Header().Set("Content-Type", o.ContentType)
		}
		if cd := q.Get("response-content-disposition"); cd != "" {
			w.Header().Set("Content-Disposition", cd)
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(o.Data)))
		if r.Method == http.MethodGet {
			_, _ = w.Write(o.Data)
		}
	case key != "" && r.Method == http.MethodDelete:
		delete(b.Objects, key)
		w.WriteHeader(http.StatusNoContent)
	default:
		cosFail(w, http.StatusMethodNotAllowed, "MethodNotAllowed", "the fake does not implement "+r.Method+" "+path+"?"+r.URL.RawQuery)
	}
}

func (f *Fake) cosACL(w http.ResponseWriter, r *http.Request, b *COSBucket) {
	const owner = "qcs::cam::uin/100000000001:uin/100000000001"
	if r.Method == http.MethodPut {
		acl := r.Header.Get("x-cos-acl")
		if acl != "private" && acl != "public-read" && acl != "public-read-write" {
			cosFail(w, http.StatusBadRequest, "InvalidArgument", "bad x-cos-acl")
			return
		}
		b.ACL = tencent.ACL{Canned: acl, Grants: map[string]string{}}
		for _, h := range []string{"x-cos-grant-read", "x-cos-grant-write", "x-cos-grant-full-control", "x-cos-grant-read-acp", "x-cos-grant-write-acp"} {
			if v := r.Header.Get(h); v != "" {
				b.ACL.Grants[h] = v
			}
		}
		return
	}
	type grantee struct {
		ID  string `xml:"ID,omitempty"`
		URI string `xml:"URI,omitempty"`
	}
	type grant struct {
		Grantee    grantee `xml:"Grantee"`
		Permission string  `xml:"Permission"`
	}
	grants := []grant{{grantee{ID: owner}, "FULL_CONTROL"}}
	all := grantee{URI: "http://cam.qcloud.com/groups/global/AllUsers"}
	switch b.ACL.Canned {
	case "public-read":
		grants = append(grants, grant{all, "READ"})
	case "public-read-write":
		grants = append(grants, grant{all, "FULL_CONTROL"})
	}
	perms := map[string]string{"x-cos-grant-read": "READ", "x-cos-grant-write": "WRITE", "x-cos-grant-full-control": "FULL_CONTROL",
		"x-cos-grant-read-acp": "READ_ACP", "x-cos-grant-write-acp": "WRITE_ACP"}
	var hs []string
	for h := range b.ACL.Grants {
		hs = append(hs, h)
	}
	sort.Strings(hs)
	for _, h := range hs {
		for _, id := range strings.Split(b.ACL.Grants[h], ",") {
			grants = append(grants, grant{grantee{ID: strings.Trim(strings.TrimPrefix(strings.TrimSpace(id), "id="), `"`)}, perms[h]})
		}
	}
	cosXML(w, struct {
		XMLName xml.Name `xml:"AccessControlPolicy"`
		Owner   struct {
			ID string `xml:"ID"`
		} `xml:"Owner"`
		Grants []grant `xml:"AccessControlList>Grant"`
	}{Owner: struct {
		ID string `xml:"ID"`
	}{owner}, Grants: grants})
}

func (f *Fake) cosList(w http.ResponseWriter, q url.Values, b *COSBucket) {
	prefix, delim, marker := q.Get("prefix"), q.Get("delimiter"), q.Get("marker")
	max, _ := strconv.Atoi(q.Get("max-keys"))
	if max <= 0 || max > 1000 {
		max = 1000
	}
	var keys []string
	for k := range b.Objects {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	enc := func(s string) string {
		if q.Get("encoding-type") == "url" {
			return url.QueryEscape(s)
		}
		return s
	}
	type content struct {
		Key          string `xml:"Key"`
		LastModified string `xml:"LastModified"`
		ETag         string `xml:"ETag"`
		Size         int    `xml:"Size"`
		StorageClass string `xml:"StorageClass"`
	}
	var contents []content
	var prefixes []string
	seen := map[string]bool{}
	count, truncated, next := 0, false, ""
	for _, k := range keys {
		if marker != "" && k <= marker {
			continue
		}
		entry, folder := k, false
		if delim != "" {
			if i := strings.Index(k[len(prefix):], delim); i >= 0 {
				entry, folder = k[:len(prefix)+i+len(delim)], true
			}
		}
		if seen[entry] {
			continue
		}
		if count == max {
			truncated = true
			break
		}
		seen[entry] = true
		count++
		next = k
		if folder {
			prefixes = append(prefixes, enc(entry))
			continue
		}
		o := b.Objects[k]
		sum := md5.Sum(o.Data)
		class := o.StorageClass
		if class == "" {
			class = "STANDARD"
		}
		contents = append(contents, content{enc(k), o.Modified.UTC().Format(time.RFC3339), fmt.Sprintf(`"%x"`, sum), len(o.Data), class})
	}
	type pfx struct {
		Prefix string `xml:"Prefix"`
	}
	var ps []pfx
	for _, p := range prefixes {
		ps = append(ps, pfx{p})
	}
	res := struct {
		XMLName      xml.Name  `xml:"ListBucketResult"`
		Name         string    `xml:"Name"`
		EncodingType string    `xml:"EncodingType,omitempty"`
		Prefix       string    `xml:"Prefix"`
		IsTruncated  bool      `xml:"IsTruncated"`
		NextMarker   string    `xml:"NextMarker,omitempty"`
		Contents     []content `xml:"Contents"`
		Prefixes     []pfx     `xml:"CommonPrefixes"`
	}{Name: b.Name, EncodingType: q.Get("encoding-type"), Prefix: enc(prefix), IsTruncated: truncated, Contents: contents, Prefixes: ps}
	if truncated {
		res.NextMarker = enc(next)
	}
	var buf bytes.Buffer
	_ = xml.NewEncoder(&buf).Encode(res)
	w.Header().Set("Content-Type", "application/xml")
	_, _ = w.Write(buf.Bytes())
}
