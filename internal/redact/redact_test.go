package redact

import (
	"regexp"
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
	for _, l := range []*lazyRE{keyValue, messageArg, varRef, secretWord, namedSecret, hexKey, keyAlphabet, flagWord} {
		l.get()
	}
	for _, p := range patterns {
		p.re.get()
	}
}

// BB-RULE-6: everyday password and key names and flags are caught: DB_PASS, MYSQL_PWD, a
// generated *_KEY, --http-password VALUE, redis-cli -a, mongosh -p, Vault tokens and a token
// given as a URL's user. Look-alikes are left alone: PWD and OLDPWD, a *_KEY that holds a
// name or a path, settings about a password, flags that take no value, and variables.
func TestRound4Redaction(t *testing.T) {
	pass := "Zq7n3HRc" + fake("", 6, alnum)
	key := "In9ntgkf" + fake("", 24, alnum)
	vault := "hvs." + fake("", 24, alnum)
	hex := fake("", 40, hexchars)
	for _, c := range []struct{ in, secret string }{
		{"export DB_PASS=" + pass + " && npm start", pass},
		{"DB_PASS='" + pass + "' npm run migrate", pass},
		{"MYSQL_PWD=" + pass + " mysql -u root shop", pass},
		{"export SMTP_PASS=" + pass, pass},
		{"docker run -e REDIS_PASS=" + pass + " app", pass},
		{"export ENCRYPTION_KEY=" + key, key},
		{"SIGNING_KEY=" + key + " ./sign.sh", key},
		{"export APP_KEY=base64:" + key + "=", key},
		{"export MASTER_KEY=" + hex, hex},
		{"kubectl create secret generic db --from-literal=DB_PASS=" + pass, pass},
		{"redis-cli -h cache.example.com -a " + pass + " ping", pass},
		{"redis-cli -a '" + pass + "' --no-auth-warning info", pass},
		{"mongosh mongodb://db.example.com -u admin -p " + pass, pass},
		{"mongodump --host db.example.com -u admin -p '" + pass + "' --out dump", pass},
		{"wget --http-password " + pass + " https://files.example.com/f", pass},
		{"curl --proxy-user u --db-password " + pass + " https://x.example", pass},
		{"gh-tool --github-token " + key + " sync", key},
		{"vault login " + vault, vault},
		{"VAULT_ADDR=https://vault.example.com vault kv get -token=" + vault + " secret/app", vault},
		{"git remote set-url origin https://" + hex + "@github.com/larkspur-labs-example/api.git", hex},
	} {
		out, n := String(c.in)
		if strings.Contains(out, c.secret) || n == 0 {
			t.Errorf("%q -> %q (%d)", c.in, out, n)
		}
	}
	for _, in := range []string{
		"PWD=/tmp make", "cd $OLDPWD", "OLDPWD=/x ls", "git commit --no-verify -m 'bypass=1'",
		"export SORT_KEY=name", "PRIMARY_KEY=id node seed.js", "export CACHE_KEY=users-v2",
		"export GPG_KEY=0xABCD1234", "export SSH_KEY=~/.ssh/id_ed25519", "export S3_KEY=uploads/2026/report.pdf",
		"export DB_PASS_FILE=/run/secrets/db", "export SSH_KEY_PATH=~/.ssh/id_rsa", "FIRST_PASS=true ./build.sh",
		"aws dynamodb query --key-condition-expression 'pk = :v'",
		"mongosh -u admin -p --authenticationDatabase admin", "redis-cli -a $REDIS_PASSWORD ping",
		"pytest --pass-through x", "curl https://user@example.com/x", "git clone https://git@example.com/larkspur-labs/api.git",
		"pg_dump --no-password -h db.example.com app", "psql -w --no-password app",
		"ansible-playbook site.yml --ask-vault-password", "gh auth login --with-token < token.txt",
		"aws secretsmanager get-secret-value --secret-id prod/db", "docker login --password-stdin ghcr.io",
		"export SIGNING_KEY=$SIGNING_KEY_B64", "export APP_KEY=${APP_KEY:-}", "hvs.short",
		`git commit -m "redis-cli -a is how you pass the password"`,
	} {
		if out, n := String(in); out != in || n != 0 {
			t.Errorf("%q -> %q", in, out)
		}
	}
}

// QA2-L5-3: a MongoDB tool counts only where it runs as a command, and a flag followed by
// another flag has no value, so everyday commands aren't redacted. Each form that runs mongosh
// or passes a password is still caught.
func TestRound5Redaction(t *testing.T) {
	pass := "Zq7n3HRc" + fake("", 6, alnum)
	for _, c := range []struct{ in, secret string }{
		{"docker exec -it db mongosh -u admin -p " + pass, pass},
		{"docker exec -it -u root mongo mongosh admin -p '" + pass + "'", pass},
		{"kubectl exec -it mongo-0 -- mongosh -u admin -p " + pass, pass},
		{"sudo mongosh -u admin -p " + pass, pass},
		{"/usr/local/bin/mongosh -u admin -p " + pass, pass},
		{"MONGO_URL=x mongosh -u admin -p " + pass, pass},
		{"cd backup && mongodump -u admin -p " + pass + " --out dump", pass},
		{"echo ok; mongorestore -u admin -p " + pass, pass},
		{"OUT=$(mongoexport -u admin -p " + pass + " -c users)", pass},
		{"tool --db-password=-" + pass + " sync", pass},
		{"tool --db-password '-" + pass + "' sync", pass},
		{"docker login -u ci --password " + pass + " registry.example.com", pass},
	} {
		out, n := String(c.in)
		if strings.Contains(out, c.secret) || n == 0 {
			t.Errorf("%q -> %q (%d)", c.in, out, n)
		}
	}
	for _, in := range []string{
		"docker run --name mongo -p 27017:27017 -d mongo:7",
		"docker run -d --name mongo -p 27017:27017 -e MONGO_INITDB_DATABASE=app mongo:7",
		"podman run --rm --name mongodb -p 27017:27017 mongo",
		"kubectl exec mongo-0 -- ls -p data",
		"kubectl port-forward svc/mongo -p 27017",
		"mysql --skip-password -u root shop",
		"mysqldump --skip-password -h db.example.com shop",
		"pg_dump --no-password -h db.example.com app",
		"git commit -m 'mongosh -p is how you pass it'",
	} {
		if out, n := String(in); out != in || n != 0 {
			t.Errorf("%q -> %q (%d)", in, out, n)
		}
	}
}

// QA3-REC-3 and QA4-REC-1: a secret given after a space to a password, token, secret or key
// flag is stripped even when it starts with "-", as about 1 in 64 URL-safe base64 keys does.
// 0.1.1's own flags (--password, --api-key and the rest of v011Flags) always take the next
// word, as 0.1.1 did, so a human or lowercase hyphenated secret is stripped too. For a flag with
// a word before the name, --no-, --ask-, --prompt- and --with- take no value, and otherwise a
// value that reads as a flag (-u, -uroot, -P3306, --verbose, --host=…) means it took none. A lone
// "-" or "--", a value made only of < or >, and a docker build --secret spec hold no secret.
// v011Flags are the flag names 0.1.1's pattern matched (410db01, redact.go), written out here
// rather than read from secretFlags, so dropping a name from the code fails this test (QA5-REC-2).
var v011Flags = []string{"password", "passwd", "secret", "token", "api-key", "apikey", "access-key", "account-key", "client-secret", "auth-token"}

func TestSecretFlagsKeep011sNames(t *testing.T) {
	have := map[string]bool{}
	for _, n := range secretFlags {
		have[n] = true
	}
	for _, n := range v011Flags {
		if !have[n] {
			t.Errorf("secretFlags lacks %q, which 0.1.1 stripped the next word after", n)
		}
	}
}

func TestFlagValueThatStartsWithADash(t *testing.T) {
	key := "-Hk3pQ9xZ2mLw7Rt5Vb8N"
	pass := "-Zq7n3HRc" + fake("", 6, alnum) + "d1"
	hex := "-a" + fake("", 39, hexchars)
	upperFirst := "-A" + fake("", 6, "abcdefghjkmnpqrstuvwxyz") + "3" + fake("", 8, "abcdefghjkmnpqrstuvwxyz")
	digitFirst := "-7" + fake("", 6, "abcdefghjkmnpqrstuvwxyz")
	cases := []struct{ in, secret string }{
		{"deploy --api-key " + key + " sync", key},
		{"app --client-secret " + key, key},
		{"app --secret " + key, key},
		{"tool --password " + pass + " sync", pass},
		{"tool --access-key " + key + " --region us-east-1", key},
		{"tool --db-password " + pass, pass},
		{"tool --api-key " + hex, hex},
		{"tool --client-secret " + upperFirst + " sync", upperFirst},
		{"tool --secret " + digitFirst + " sync", digitFirst},
		// QA4-REC-1: values that read like a flag, given to one of 0.1.1's own flags.
		{"deploy --password -hunter22 prod", "hunter22"},
		{"deploy --api-key -k3pq-9xz2-mlw7-rt5v prod", "k3pq-9xz2-mlw7-rt5v"},
		{"tool --password -Secret123 sync", "Secret123"},
		{"deploy --PASSWORD -hunter22 prod", "hunter22"},
		{"deploy --Api-Key -k3pq-9xz2-mlw7-rt5v prod", "k3pq-9xz2-mlw7-rt5v"},
		{"mysql --password -u root shop", "-u"}, // over-redacted, as 0.1.1 did
	}
	for _, name := range v011Flags {
		cases = append(cases, struct{ in, secret string }{"deploy --" + name + " -lark-spur-9 prod", "lark-spur-9"})
	}
	for _, c := range cases {
		out, n := String(c.in)
		if strings.Contains(out, c.secret) || n == 0 {
			t.Errorf("%q -> %q (%d)", c.in, out, n)
		}
	}
	for _, in := range []string{
		"mysql --skip-password -u root shop",
		"mysql --skip-password -uroot -P3306 shop",
		"mysql --skip-password -h db.example.com shop",
		"mysql --skip-password --verbose shop",
		"mysql --skip-password --host=db.example.com --port=3306 shop",
		"mysql --skip-password --default-character-set=utf8mb4 shop",
		"docker run --name mongo -p 27017:27017 -d mongo:7",
		"tool --token - < token.txt",
		"tool --token -- run",
		"tool --password - < pass.txt",
		"mysql --skip-password -u root --password-stdin shop",
		"tool --db-password -v sync",
	} {
		if out, n := String(in); out != in || n != 0 {
			t.Errorf("%q -> %q (%d)", in, out, n)
		}
	}
}

// QA4-REC-1: a flag that another flag took as its value is still judged on its own, so a secret
// given to it is stripped. In mysql --skip-password --password VALUE, --skip-password takes no
// value and --password takes VALUE, as 0.1.1 read it. In --password --token VALUE, 0.1.1 stripped
// only --token; VALUE is now stripped too.
func TestFlagTakenAsAnotherFlagsValue(t *testing.T) {
	for _, c := range []struct{ in, secret string }{
		{"mysql --skip-password --password hunter22 shop", "hunter22"},
		{"pg_dump --no-password --token k3pq9xz2mlw7 app", "k3pq9xz2mlw7"},
		{"tool --db-password --api-key -hunter22 sync", "hunter22"},
		{"tool --ask-password --secret 'Lark Spur 9' sync", "Lark Spur 9"},
		{"tool --password --token k3pq9xz2mlw7 sync", "k3pq9xz2mlw7"},
		// QA6-REC-1: a 0.1.1 flag inside the value of a flag only 0.1.2 matches, which 0.1.1 found.
		{`remote-run --no-password "curl -s --token Zq7nHRcK2pLw9x https://api.example.com"`, "Zq7nHRcK2pLw9x"},
		{`tool --ask-password "deploy --api-key Zq7nHRcK2pLw9x"`, "Zq7nHRcK2pLw9x"},
		{`gh auth login --with-token "--password Zq7nHRcK2pLw9x"`, "Zq7nHRcK2pLw9x"},
		{`sed -i 's/--db-password old1/--password N3wSecret9/' deploy.sh`, "N3wSecret9"},
		{"tool --db-password=--token Zq7nHRcK2pLw9x", "Zq7nHRcK2pLw9x"},
		{"tool --no-password=--token Zq7nHRcK2pLw9x", "Zq7nHRcK2pLw9x"},
		{"tool --db-password abc--token Zq7nHRcK2pLw9x", "Zq7nHRcK2pLw9x"},
	} {
		out, n := String(c.in)
		if strings.Contains(out, c.secret) || n == 0 {
			t.Errorf("%q -> %q (%d)", c.in, out, n)
		}
	}
	for _, in := range []string{
		"mysql --skip-password --verbose shop",
		"pg_dump --no-password --host db.example.com app",
		"gh auth login --with-token < token.txt",
		`git commit -m "use --skip-password --password x"`,
	} {
		if out, n := String(in); out != in || n != 0 {
			t.Errorf("%q -> %q (%d)", in, out, n)
		}
	}
	// QA5-REC-3: a flag written inside a value that was stripped isn't judged again, so a sed
	// edit of a script's flag keeps its replacement text, as in 0.1.1.
	in, want := `sed -i 's/--password x/--password y/' run.sh`, `sed -i 's/--password [redacted] y/' run.sh`
	if out, n := String(in); out != want || n != 1 {
		t.Errorf("%q -> %q (%d), want %q (1)", in, out, n, want)
	}
}

// QA6-REC-1: every value 0.1.1's flag expression stripped is still stripped, wherever 0.1.2's
// wider expression (flags with a word before the name) matched first. 0.1.1's expression and its
// one veto, a docker build --secret spec, are written out here as 0.1.1 had them (410db01), and
// run over flags inside other flags' values: quoted commands, = forms, glued and sed forms. A lone
// "-" or "--", or a value made only of < or >, holds no secret and is no longer stripped. 0.1.1
// ran its "token VALUE" pattern first, so a flag inside a value that pattern stripped was never
// seen by its flag expression (sed 's/--with-token old1/--access-key KEY/' kept KEY); such
// flags are skipped here, as 0.1.1 skipped them.
func TestFlagsStripEverything011Stripped(t *testing.T) {
	tokenWord := regexp.MustCompile(`(?i)\b(?:bearer|token)\s+([A-Za-z0-9_\-\.=+/]{16,})`)
	v011 := regexp.MustCompile(`(?i)--(?:password|passwd|secret|token|api-key|apikey|access-key|account-key|client-secret|auth-token)(?:=|\s+)(?:'([^']*)'|"([^"]*)"|([^\s'"&;|]+))`)
	outers := []string{"--password", "--token", "--no-password", "--ask-password", "--prompt-password", "--with-token", "--db-password", "--skip-password", "--github-token"}
	values := []string{"Zq7nHRcK2pLw9x", "hunter22", "-hunter22", "k3pq-9xz2-mlw7", "-Hk3pQ9xZ2mLw7Rt5Vb8N"}
	forms := []func(o, i, v string) string{
		func(o, i, v string) string {
			return "run " + o + ` "curl -s ` + i + " " + v + ` https://api.example.com"`
		},
		func(o, i, v string) string { return "run " + o + " '" + i + " " + v + "' sync" },
		func(o, i, v string) string { return "run " + o + "=" + i + " " + v },
		func(o, i, v string) string { return "run " + o + " " + i + " " + v + " sync" },
		func(o, i, v string) string { return "run " + o + " abc" + i + " " + v },
		func(o, i, v string) string { return "sed -i 's/" + o + " old1/" + i + " " + v + "/' deploy.sh" },
		func(o, i, v string) string { return "run " + o + "=" + i + "=" + v },
	}
	n := 0
	for _, o := range outers {
		for _, name := range v011Flags {
			for _, v := range values {
				for _, form := range forms {
					in := form(o, "--"+name, v)
					out, _ := String(in)
					stripped := strippedBytes(t, in, out)
					var swallowed [][2]int
					for _, m := range tokenWord.FindAllStringSubmatchIndex(in, -1) {
						if strings.ContainsAny(in[m[2]:m[3]], "0123456789") {
							swallowed = append(swallowed, [2]int{m[2], m[3]})
						}
					}
				flags:
					for _, m := range v011.FindAllStringSubmatchIndex(in, -1) {
						for _, sw := range swallowed {
							if m[0] >= sw[0] && m[0] < sw[1] {
								continue flags
							}
						}
						var vs, ve int
						for g := 1; g <= 3; g++ {
							if m[2*g] >= 0 {
								vs, ve = m[2*g], m[2*g+1]
							}
						}
						val := in[vs:ve]
						if strings.Trim(val, "<>") == "" || val == "-" || val == "--" || dockerSecretSpec("", nil, val) {
							continue
						}
						n++
						for i := vs; i < ve; i++ {
							if !stripped[i] {
								t.Errorf("%q -> %q keeps %q, which 0.1.1 stripped", in, out, val)
								break
							}
						}
					}
				}
			}
		}
	}
	if n < 2000 {
		t.Fatalf("checked only %d values", n)
	}
}

// strippedBytes reports which bytes of in were replaced by a Marker in out, by aligning out's
// text between markers with in.
func strippedBytes(t *testing.T, in, out string) []bool {
	t.Helper()
	parts := strings.Split(out, Marker)
	var expr strings.Builder
	expr.WriteString("(?s)^")
	for i, p := range parts {
		expr.WriteString(regexp.QuoteMeta(p))
		if i < len(parts)-1 {
			expr.WriteString("(.+?)")
		}
	}
	expr.WriteString("$")
	m := regexp.MustCompile(expr.String()).FindStringSubmatchIndex(in)
	if m == nil {
		t.Fatalf("can't align %q with %q", out, in)
	}
	got := make([]bool, len(in))
	for g := 1; g < len(parts); g++ {
		for i := m[2*g]; i < m[2*g+1]; i++ {
			got[i] = true
		}
	}
	return got
}

// QA7-REC-1: a secret pair after a pair named only by a generic KEY, in a comma list or a quoted
// value, is stripped, as 0.1.1 stripped it: the wider key=value expression's match runs past it
// without stripping, and 0.1.1's expression, run over the same text, finds it.
func TestSecretPairAfterAGenericKeyPair(t *testing.T) {
	wrappers := []string{
		`gcloud run deploy api --set-env-vars "%s"`,
		`gcloud run deploy api --update-env-vars %s`,
		`aws lambda update-function-configuration --function-name api --environment "Variables={%s}"`,
		`helm upgrade --install api ./chart --set %s`,
		`docker run -e '%s' app`,
	}
	generic := []string{"CACHE_KEY=prod", "SORT_KEY=name", "PRIMARY_KEY=id", "service.cache_key=v1", "STORAGE_KEY=users-v2"}
	secrets := []struct{ pair, secret string }{
		{"DB_PASSWORD=hunter2x", "hunter2x"},
		{"API_TOKEN=Zq7nHRcK2pLw9x", "Zq7nHRcK2pLw9x"},
		{"postgresql.auth.password=Lark5pur99", "Lark5pur99"},
		{"CLIENT_SECRET=k3pq9xz2mlw7", "k3pq9xz2mlw7"},
	}
	for _, w := range wrappers {
		for _, g := range generic {
			for _, sc := range secrets {
				for _, list := range []string{g + "," + sc.pair, g + "," + sc.pair + ",REGION=us-east-1"} {
					in := strings.Replace(w, "%s", list, 1)
					out, n := String(in)
					if strings.Contains(out, sc.secret) || n == 0 {
						t.Errorf("%q -> %q (%d)", in, out, n)
					}
					if !strings.Contains(out, g) {
						t.Errorf("%q -> %q changed %q, which names no secret", in, out, g)
					}
				}
			}
		}
	}
	for _, c := range []struct{ in, secret string }{
		{`gcloud run deploy api --set-env-vars=^:^CACHE_KEY=prod:DB_PASSWORD=hunter2x`, "hunter2x"},
		{`export STORAGE_KEY="DefaultEndpointsProtocol=https;AccountName=larkspur;AccountKey=Zq7nHRcK2pLw9xHk3pQ9xZ2mLw7Rt5Vb8N=="`, "Zq7nHRcK2pLw9xHk3pQ9xZ2mLw7Rt5Vb8N"},
		{`CACHE_KEY="session token=Zq7nHRcK2pLw9x" ./run.sh`, "Zq7nHRcK2pLw9x"},
		// A stripped pair after the swallowed one: the matches are taken in the order their values
		// start, so the earlier secret isn't written out before it's judged.
		{`gcloud run deploy api --set-env-vars "CACHE_KEY=prod,DB_PASSWORD=hunter2x" --update-secrets API_KEY=Zq7nHRcK2pLw9x`, "hunter2x"},
		{`gcloud run deploy api --set-env-vars "CACHE_KEY=prod,DB_PASSWORD=hunter2x" --update-secrets API_KEY=Zq7nHRcK2pLw9x`, "Zq7nHRcK2pLw9x"},
		// QA8-REC-1: a key read as a setting only since 0.1.2 (SECRET_KEY_FILE) is judged as 0.1.1
		// judged it when 0.1.1's expression matches it, so the pair after it is stripped too.
		{`gcloud run deploy api --set-env-vars SECRET_KEY_FILE=/secrets/django-key,DB_PASSWORD=hunter2x`, "hunter2x"},
		{`aws lambda update-function-configuration --function-name api --environment "Variables={JWT_SECRET_KEY_PATH=/opt/jwt.pem,DB_PASSWORD=hunter2x}"`, "hunter2x"},
		{`helm upgrade --install api ./chart --set app.secret_key_file=/etc/app/key,postgresql.auth.password=hunter2x`, "hunter2x"},
		{`gcloud run deploy api --set-env-vars=^:^SECRET_KEY_PATH=/k:API_TOKEN=Zq7nHRcK2pLw9x`, "Zq7nHRcK2pLw9x"},
		{`gcloud run deploy api --set-env-vars SECRET_KEY_LENGTH=50,DB_PASSWORD=hunter2x`, "hunter2x"},
		{`gcloud run deploy api --set-env-vars TOKEN_KEY_TYPE=RS256,CLIENT_SECRET=hunter2x`, "hunter2x"},
	} {
		if out, n := String(c.in); strings.Contains(out, c.secret) || n == 0 {
			t.Errorf("%q -> %q (%d)", c.in, out, n)
		}
	}
	for _, in := range []string{
		`gcloud run deploy api --set-env-vars "SORT_KEY=name,REGION=us-east-1"`,
		`helm upgrade --install api ./chart --set service.cache_key=v1,replicaCount=2`,
		`aws lambda update-function-configuration --environment "Variables={PRIMARY_KEY=id,CACHE_KEY=users-v2}"`,
	} {
		if out, n := String(in); out != in || n != 0 {
			t.Errorf("%q -> %q (%d)", in, out, n)
		}
	}
}

// QA8-REC-3: a pair both key=value expressions match is counted once, and a value that starts
// inside one already stripped takes the rest of it.
func TestMergedKeyValueCounts(t *testing.T) {
	for _, c := range []struct {
		in, want string
		n        int
	}{
		{"DB_PASSWORD=hunter2x ./run", "DB_PASSWORD=[redacted] ./run", 1},
		{`gcloud run deploy api --set-env-vars "CACHE_KEY=prod,DB_PASSWORD=hunter2x"`, `gcloud run deploy api --set-env-vars "CACHE_KEY=prod,DB_PASSWORD=[redacted]"`, 1},
		{`APP_KEY="base64:Zq7nHRcK2pLw9xAb token='abc" tail' end`, `APP_KEY="[redacted]' end`, 1},
	} {
		if out, n := String(c.in); out != c.want || n != c.n {
			t.Errorf("%q -> %q (%d), want %q (%d)", c.in, out, n, c.want, c.n)
		}
	}
}

// QA7-REC-4: a later flag and a value straddling a quote keep no secret 0.1.1 stripped. A flag
// match can't start inside another's flag name, so sorting the two flag expressions' matches by
// match or by value gives the same order (QA8-REC-2); this guards that and the straddle.
func TestMergedFlagMatchesInOrder(t *testing.T) {
	for _, c := range []struct{ in, secret string }{
		{`tool --no-password "curl --token Zq7nHRcK2pLw9x" --password hunter22`, "Zq7nHRcK2pLw9x"},
		{`tool --no-password "curl --token Zq7nHRcK2pLw9x" --password hunter22`, "hunter22"},
		{`tool --db-password "a --token 'b" Zq7nHRcK2pLw9x'`, "Zq7nHRcK2pLw9x"},
		{`tool --DB-PASSWORD "a --TOKEN Zq7nHRcK2pLw9x" --Password hunter22`, "Zq7nHRcK2pLw9x"},
		{`tool --DB-PASSWORD "a --TOKEN Zq7nHRcK2pLw9x" --Password hunter22`, "hunter22"},
	} {
		if out, n := String(c.in); strings.Contains(out, c.secret) || n == 0 {
			t.Errorf("%q -> %q (%d)", c.in, out, n)
		}
	}
}

// QA5-REC-1: the flag search that lets one flag be taken as another's value stays linear. Chains
// of flags whose values run to the next blank took seconds at 64 KB when every match was searched
// again from its value; 0.1.1 takes about 30 ms.
func TestFlagSearchStaysLinear(t *testing.T) {
	if raceDetector {
		t.Skip("timing isn't meaningful under the race detector")
	}
	for _, link := range []string{"--token=", "--no-password=", "--password=a", "--no-password ", "--skip-password --password=", "--db-password -"} {
		in := "x " + strings.Repeat(link, 65536/len(link))
		start := time.Now()
		String(in)
		if el := time.Since(start); el > 500*time.Millisecond {
			t.Errorf("%q x %d took %v", link, 65536/len(link), el)
		}
	}
}
