package plugin

import (
	"embed"
	"encoding/base64"
	"encoding/json"
	"regexp"
)

//go:embed web/ui.html web/*.css web/*.js web/locales/*.json
var uiFiles embed.FS

var uiHTML = buildUI()

var pluginLogo = "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte(pluginIconSVG))

var uiInclude = regexp.MustCompile(`/\*([a-z0-9-]+\.(?:css|js))\*/`)

func buildUI() []byte {
	read := func(name string) []byte {
		data, err := uiFiles.ReadFile("web/" + name)
		if err != nil {
			panic(err)
		}
		return data
	}
	catalogs := map[string]map[string]string{}
	for _, language := range []string{"en", "zh-CN"} {
		var entries map[string]string
		if err := json.Unmarshal(read("locales/"+language+".json"), &entries); err != nil {
			panic(err)
		}
		catalogs[language] = entries
	}
	data, _ := json.Marshal(catalogs)
	return uiInclude.ReplaceAllFunc(read("ui.html"), func(marker []byte) []byte {
		name := string(uiInclude.FindSubmatch(marker)[1])
		if name != "i18n.js" {
			return read(name)
		}
		script := append([]byte("const BILLING_MESSAGES = "), data...)
		script = append(script, ';', '\n')
		return append(script, read(name)...)
	})
}

const pluginIconSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="24.57 25.47 461.94 459.4" fill="#72787c">
  <style>
    /* Hosts render the logo through an img element, where currentColor resolves
       to black rather than the sidebar tone. These two tones are the closest a
       static asset gets to the muted icon colours of both CPAMC and CPAMP. */
    @media (prefers-color-scheme: dark) { :root { fill: #9c9d9b; } }
  </style>
  <path d="M 311.98 32.34 C 224.78 9.12 134.48 46.79 78.75 115.42 C 43.14 158.76 24.57 205.2 24.57 256.8 C 24.57 381.67 126.22 484.87 255.74 484.87 C 286.7 484.87 316.62 479.2 345 467.33 Q 350.16 465.26 348.1 460.62 L 336.09 428.88 Q 333.94 424.03 329.08 425.65 C 305.89 434.82 280.55 439.13 255.75 439.13 C 151.13 439.13 68.63 357.17 68.63 256.87 C 68.63 156.04 151.68 73.54 255.75 73.54 C 270.84 73.54 285.41 74.61 299.42 77.85 Q 305.36 79.47 306.97 73.54 L 315.59 39.05 Q 317.14 33.89 311.98 32.34 Z"/>
  <path d="M 335.2 39.56 C 386.8 58.14 429.11 94.78 456.46 142.25 Q 459.04 146.89 453.88 149.99 L 424.52 166.28 Q 419.67 168.98 416.97 163.59 C 395.95 127.46 363.05 99.96 323.68 84.87 Q 318.29 83.25 319.91 77.85 L 329.01 43.69 Q 330.56 38.02 335.2 39.56 Z"/>
  <path d="M 459.56 162.89 Q 465.23 159.79 467.3 165.47 C 482.26 199.01 489.48 238.74 485.36 274.34 Q 485.36 280.02 480.2 278.99 L 448.25 277.37 Q 442.31 277.37 442.86 271.97 C 445.01 241.77 439.08 212.12 427.76 186.77 Q 425.06 181.38 430.45 178.69 L 459.56 162.89 Z"/>
  <path d="M 446.63 290.85 L 480.71 292.92 Q 485.87 293.44 484.84 299.11 C 472.97 366.19 430.66 423.47 370.29 455.46 Q 366.16 457.52 364.1 452.88 L 349.57 424.57 Q 346.87 419.72 351.72 416.49 C 398.1 388.44 431.53 345.3 440.16 296.24 Q 441.24 290.3 446.63 290.85 Z"/>
  <path fill-rule="evenodd" d="M 240.15 231.85 C 232.34 199.53 251.85 167.2 281.38 151.05 C 314.82 129.87 354.94 141.01 378.91 165.54 C 399.53 187.27 405.66 220.15 392.29 250.8 C 373.9 292.04 324.85 309.31 283.06 288.14 Q 279.71 285.91 277.49 288.14 L 253.52 306.52 Q 251.3 308.2 251.3 311.55 L 251.3 324.37 Q 251.3 329.94 245.72 329.94 L 223.98 329.94 Q 220.65 329.94 220.65 333.28 L 220.65 348.32 Q 220.65 353.34 215.62 353.34 L 198.9 353.34 Q 195.57 353.34 195.57 356.68 L 195.57 377.31 Q 195.57 382.32 190.55 382.32 L 165.47 382.32 Q 162.13 382.32 159.34 379.53 L 142.63 362.81 Q 139.84 360.59 139.84 356.68 L 139.84 326.03 Q 139.84 322.69 142.63 320.46 L 238.48 237.99 Q 242.94 235.2 240.15 231.85 Z M 367.21 208.45 C 367.21 224.61 354.39 237.99 337.67 237.99 C 321.51 237.99 308.7 225.17 308.7 208.45 C 308.7 192.28 321.51 178.91 337.67 178.91 C 353.83 178.91 367.21 192.28 367.21 208.45 Z"/>
</svg>
`
