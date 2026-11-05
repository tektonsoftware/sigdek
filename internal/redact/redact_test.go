package redact

import (
	"strings"
	"testing"
	"time"
)

// Fake secrets are assembled at run time so the repository never contains a string that
// a secret scanner (or a customer's) would flag. Each case lists the values that must not survive.
func fake(prefix string, n int, alphabet string) string {
	var b strings.Builder
	b.WriteString(prefix)
	for i := 0; i < n; i++ {
		b.WriteByte(alphabet[(i*7+3)%len(alphabet)])
	}
	return b.String()
}

const (
	upperDigits = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"
	alnum       = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	hexchars    = "0123456789abcdef"
)

func TestSecretsNeverSurvive(t *testing.T) {
	aws := fake("AKIA", 16, upperDigits)
	ghp := fake("ghp_", 36, alnum)
	ghpat := fake("github_pat_", 60, alnum+"_")
	glpat := fake("glpat-", 20, alnum)
	slack := fake("xoxb-", 40, alnum+"-")
	stripe := fake("sk_"+"live_", 24, alnum)
	whsec := fake("whsec_", 32, alnum)
	anthropic := fake("sk-ant-api03-", 48, alnum)
	openai := fake("sk-proj-", 48, alnum)
	google := fake("AIza", 35, alnum)
	npm := fake("npm_", 36, alnum)
	jwt := "eyJ" + fake("", 20, alnum) + ".eyJ" + fake("", 30, alnum) + "." + fake("", 40, alnum)
	do := fake("dop_v1_", 64, hexchars)
	hf := fake("hf_", 34, alnum)
	pem := "-----BEGIN RSA PRIVATE KEY-----\nMIIEow" + fake("", 60, alnum) + "\n-----END RSA PRIVATE KEY-----"

	cases := []struct {
		in      string
		secrets []string
	}{
		{"aws configure set aws_access_key_id " + aws, []string{aws}},
		{"git clone https://" + ghp + "@github.com/acme/api.git", []string{ghp}},
		{"GH_TOKEN=" + ghpat + " gh pr list", []string{ghpat}},
		{"curl -H 'PRIVATE-TOKEN: " + glpat + "' https://gitlab.example.com/api/v4/projects", []string{glpat}},
		{"curl -X POST -H 'Authorization: Bearer " + slack + "' https://slack.com/api/chat.postMessage", []string{slack}},
		{"stripe listen --api-key " + stripe, []string{stripe}},
		{"export STRIPE_WEBHOOK_SECRET=" + whsec, []string{whsec}},
		{"ANTHROPIC_API_KEY=" + anthropic + " claude -p hi", []string{anthropic}},
		{"OPENAI_API_KEY=\"" + openai + "\" python run.py", []string{openai}},
		{"curl 'https://maps.googleapis.com/maps/api/geocode/json?address=x&key=" + google + "'", []string{google}},
		{"echo //registry.npmjs.org/:_authToken=" + npm + " > .npmrc", []string{npm}},
		{"curl -H \"Authorization: Bearer " + jwt + "\" https://api.example.com/me", []string{jwt}},
		{"doctl auth init -t " + do, []string{do}},
		{"huggingface-cli login --token " + hf, []string{hf}},
		{"psql postgres://admin:S3cr3tPassw0rd@db.example.com:5432/app", []string{"S3cr3tPassw0rd"}},
		{"mysql -u root -pHunter2Hunter2 -h db app", []string{"Hunter2Hunter2"}},
		{"DB_PASSWORD='correct horse battery staple' ./migrate", []string{"correct horse battery staple"}},
		{"docker login -u ci --password ZXhhbXBsZXBhc3M registry.example.com", []string{"ZXhhbXBsZXBhc3M"}},
		{"curl -u deploy:tok3n https://example.com && curl 'https://api.example.com/v1?client_secret=abc123def456&page=2'", []string{"abc123def456"}},
		{"printf '%s' '" + pem + "' > key.pem", []string{"MIIEow"}},
	}
	for _, c := range cases {
		out, n := String(c.in)
		for _, s := range c.secrets {
			if strings.Contains(out, s) {
				t.Errorf("secret survived\n  in:  %s\n  out: %s", c.in, out)
			}
		}
		if n == 0 {
			t.Errorf("no redactions counted for %q", c.in)
		}
		if !strings.Contains(out, Marker) {
			t.Errorf("marker missing in %q", out)
		}
	}
}

func TestOrdinaryCommandsAreUntouched(t *testing.T) {
	for _, s := range []string{
		"npm test",
		"git status",
		"git commit -m \"fix token refresh when session expires\"",
		"git commit --author=\"Dev <dev@example.com>\" -m wip",
		"ls -la src/auth",
		"go test ./internal/... -run TestAuth",
		"curl https://example.com/api?page=2&sort=asc",
		"grep -rn password src/ --include=*.go",
		"kubectl get pods -n payments",
		"echo $HOME",
	} {
		if out, n := String(s); out != s || n != 0 {
			t.Errorf("changed an ordinary command\n  in:  %s\n  out: %s", s, out)
		}
	}
}

func TestIdempotent(t *testing.T) {
	in := "TOKEN=abc123456789 ./run"
	once, _ := String(in)
	twice, n := String(once)
	if once != twice || n != 0 {
		t.Fatalf("second pass changed output: %q -> %q (%d)", once, twice, n)
	}
}

// QA REC-8: formats the first version missed. Each case lists the values that must not survive.
func TestMoreSecretFormats(t *testing.T) {
	xapp := fake("xapp-1-A0", 40, alnum+"-")
	pgp := "-----BEGIN PGP PRIVATE KEY BLOCK-----\n\nlQOYBF" + fake("", 60, alnum) + "\n-----END PGP PRIVATE KEY BLOCK-----"
	azure := fake("", 86, alnum) + "=="
	webhook := "T0" + fake("", 9, upperDigits) + "/B0" + fake("", 9, upperDigits) + "/" + fake("", 24, alnum)
	cases := []struct {
		in      string
		secrets []string
	}{
		{"curl -u deploy:tok3nValue https://example.com/api", []string{"tok3nValue"}},
		{"curl --user 'ci:s3cr3t-pass' https://example.com", []string{"s3cr3t-pass"}},
		{"curl -uadmin:hunter22 https://example.com", []string{"hunter22"}},
		{`curl -d '{"password":"hunter2hunter2","user":"dev"}' https://example.com/login`, []string{"hunter2hunter2"}},
		{`echo '{"api_key": "abc123def456ghi"}' > config.json`, []string{"abc123def456ghi"}},
		{"aws configure set aws_secret_access_key wJalrXUtnFEMIK7MDENGbPxRfiCYEXAMPLEKEY", []string{"wJalrXUtnFEMIK7MDENGbPxRfiCYEXAMPLEKEY"}},
		{"aws configure set default.aws_session_token FQoGZXIvYXdzEXAMPLE", []string{"FQoGZXIvYXdzEXAMPLE"}},
		{"redis-cli -u redis://:r3disPass@cache.internal:6379", []string{"r3disPass"}},
		{"psql postgres://admin:p@ss/w0rd@db.example.com:5432/app", []string{"p@ss/w0rd", "ss/w0rd"}},
		{"slack-bolt --app-token " + xapp, []string{xapp}},
		{"curl -X POST -d '{}' https://hooks.slack.com/services/" + webhook, []string{webhook}},
		{"echo '" + pgp + "' | gpg --import", []string{"lQOYBF"}},
		{`az storage blob list --connection-string "DefaultEndpointsProtocol=https;AccountName=acme;AccountKey=` + azure + `;EndpointSuffix=core.windows.net"`, []string{azure}},
		{"npm config set //registry.example.com/:_authToken abc123def456", []string{"abc123def456"}},
		{"yarn config set _authToken s3cr3tT0ken", []string{"s3cr3tT0ken"}},
		{"sshpass -p 'Sup3r Secret' ssh deploy@host", []string{"Sup3r Secret"}},
		{"sshpass -p hunter2x scp a host:", []string{"hunter2x"}},
		{"htpasswd -b -c .htpasswd admin Adm1nPass", []string{"Adm1nPass"}},
		{"htpasswd -nb admin Adm1nPass2", []string{"Adm1nPass2"}},
	}
	for _, c := range cases {
		out, n := String(c.in)
		for _, s := range c.secrets {
			if strings.Contains(out, s) {
				t.Errorf("secret survived\n  in:  %s\n  out: %s", c.in, out)
			}
		}
		if n == 0 {
			t.Errorf("no redactions counted for %q", c.in)
		}
	}
}

// QA REC-8: ordinary commands that only look like secrets must come through unchanged.
func TestLookAlikesAreUntouched(t *testing.T) {
	for _, s := range []string{
		`rg 'token=' -g '*.ts'`,
		`grep -rn "password:" config/`,
		`sed 's/password=old/password=new/' app.conf`,
		`sed -i 's/token=.*/token=REPLACE_ME/' .env.example`,
		`docker build --secret id=npmrc,src=$HOME/.npmrc .`,
		`git commit -m "token: refresh before the session expires"`,
		`git commit -am "secret: rotate the signing key monthly"`,
		`git tag -a v1.2 -m 'fix: password: field validation'`,
		`git checkout -b feature/sk-update-billing-webhooks`,
		`git push origin feature/sk-ant-migration-cleanup`,
		`curl http://localhost:5173/@vite/client`,
		`curl https://example.com:8443/users/@me`,
		`export GITHUB_TOKEN=$GH_TOKEN`,
		`curl -H "Authorization: Bearer $API_TOKEN" https://api.example.com`,
		`is_auth value`,
		`git push -u origin main`,
	} {
		if out, n := String(s); out != s || n != 0 {
			t.Errorf("changed an ordinary command\n  in:  %s\n  out: %s", s, out)
		}
	}
}

// QA REC-6: Cut redacts only what it keeps, so a megabyte command costs milliseconds, and a
// secret straddling the limit is still caught.
func TestCutIsFastAndCatchesSecretsAtTheEdge(t *testing.T) {
	big := "echo " + strings.Repeat("a b=c ", 200000)
	start := time.Now()
	out, _ := Cut(big, 4096)
	if el := time.Since(start); (el > 200*time.Millisecond && !raceDetector) || len(out) > 4096 {
		t.Fatalf("Cut took %v, %d bytes", el, len(out))
	}
	ghp := fake("ghp_", 36, alnum)
	edge := strings.Repeat("x", 4080) + " " + ghp
	out, n := Cut(edge, 4096)
	if n != 1 || strings.Contains(out, ghp[:12]) || len(out) > 4096 {
		t.Fatalf("token at the limit: n=%d tail=%q", n, out[len(out)-20:])
	}
	if out, _ := Cut("héllo", 2); out != "h" {
		t.Fatalf("cut inside a rune: %q", out)
	}
}

// QA2-REC-12: Azure SAS signatures and docker login -p are caught; grep password= and
// GitHub Actions expressions are left alone.
func TestRound2Redaction(t *testing.T) {
	sas := fake("", 43, alnum) + "%2B%2F%3D"
	pass := "S3cret" + fake("", 6, alnum)
	google := fake("AIza", 34, alnum)
	for _, c := range []struct{ in, secret string }{
		{`curl "https://acct.blob.core.windows.net/c/f?sv=2022-11-02&sig=` + sas + `"`, sas},
		{"curl 'https://bucket.s3.amazonaws.com/x?X-Amz-Signature=" + fake("", 64, hexchars) + "&X-Amz-Date=1'", fake("", 64, hexchars)},
		{"docker login -u ci -p " + pass + " registry.acme.test", pass},
		{"podman login -p '" + pass + "' quay.io", pass},
		{`curl "https://maps.googleapis.com/x?key=` + google + `"`, google},
		{"echo 'password = " + pass + "' >> app.ini", pass},
	} {
		out, n := String(c.in)
		if strings.Contains(out, c.secret) || n == 0 {
			t.Errorf("%q -> %q (%d)", c.in, out, n)
		}
	}
	for _, in := range []string{
		"grep -rn password= config/",
		"grep -rn 'token: ' src/",
		"export TOKEN=${{ secrets.TOKEN }}",
		"helm template --set token={{ .Values.token }}",
		"API_KEY=$(cat ~/.key) ./run",
		"PASSWORD=${DB_PASSWORD:-} ./run",
		"docker login -u ci --password-stdin registry.acme.test",
		"curl 'https://x.test/search?q=key&page=2'",
	} {
		if out, n := String(in); out != in || n != 0 {
			t.Errorf("%q -> %q", in, out)
		}
	}
}

// QA3-REC-8: passwords given to openssl, zip, 7z, az login, sqlcmd and gh secret set, cookies,
// and a secret echoed into --password-stdin are caught; settings about a secret, placeholder
// values, variable references and port flags are left alone.
func TestRound3Redaction(t *testing.T) {
	pass := "S3cret" + fake("", 8, alnum)
	cookie := fake("", 32, hexchars)
	for _, c := range []struct{ in, secret string }{
		{"openssl genrsa -aes256 -passout pass:" + pass + " -out k.pem 4096", pass},
		{"openssl pkcs12 -export -in c.pem -passin 'pass:" + pass + "' -out c.p12", pass},
		{"openssl enc -aes-256-cbc -k " + pass + " -in a -out b", pass},
		{"zip -P " + pass + " out.zip file", pass},
		{"unzip -P '" + pass + "' out.zip", pass},
		{"7z a -p" + pass + " out.7z dir", pass},
		{"az login -u me@acme.test -p '" + pass + "!'", pass},
		{"sqlcmd -S db -U sa -P '" + pass + "!' -Q 'select 1'", pass},
		{`curl -H "Cookie: sessionid=` + cookie + `; theme=dark" https://x.test`, cookie},
		{`curl -b "session=` + cookie + `" https://x.test`, cookie},
		{"curl --cookie session=" + cookie + " https://x.test", cookie},
		{`gh secret set DEPLOY_KEY --body "` + pass + `"`, pass},
		{"echo " + pass + " | docker login -u ci --password-stdin ghcr.io", pass},
		{"export API_TOKEN=" + pass, pass},
		{"PASSWORD=" + pass + " ./run", pass},
	} {
		out, n := String(c.in)
		if strings.Contains(out, c.secret) || n == 0 {
			t.Errorf("%q -> %q (%d)", c.in, out, n)
		}
	}
	for _, in := range []string{
		"export PASSWORD_MIN_LENGTH=12",
		"TOKEN_TTL=3600 ./run",
		"DB_PASSWORD_FILE=/run/secrets/db ./run",
		"REQUIRE_PASSWORD=true ./run",
		`echo "password: required"`,
		`curl -u "$USER:$PASS" https://x.test`,
		`git commit -m "docker login -p is deprecated; use --password-stdin"`,
		`echo "$CR_PAT" | docker login ghcr.io -u ci --password-stdin`,
		"curl -b cookies.txt -c cookies.txt https://x.test",
		`grep -n "Cookie:" access.log`,
		"openssl genrsa -aes256 -passout env:KEY_PASS -out k.pem",
		"ssh -p 2222 deploy@host", "scp -P 2222 f host:", "docker run -p 8080:80 nginx", "zip -r out.zip dir",
	} {
		if out, n := String(in); out != in || n != 0 {
			t.Errorf("%q -> %q", in, out)
		}
	}
}

// Expressions are compiled when first used, not at start-up; every one of them compiles.
func TestEveryExpressionCompiles(t *testing.T) {
	for _, l := range []*lazyRE{keyValue, messageArg, varRef, secretWord} {
		l.get()
	}
	for _, p := range patterns {
		p.re.get()
	}
}
