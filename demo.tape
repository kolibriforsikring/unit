Output "demo.gif"

Set FontSize 18
Set Width 1200
Set Height 600
Set Padding 20
Set WindowBar Transparent
Set Theme "TokyoNight"

# Start the demo
Type "# Install unit" Sleep 500ms Enter
Type "go install github.com/stianh/unit/cmd/unit@latest" Sleep 1s Enter
Sleep 1s
Clear

Type "# 1. Prepare the remote server" Sleep 500ms Enter
Type "unit setup -e prod" Sleep 500ms Enter
Sleep 2s

Type "# 2. Deploy the statically linked binary" Sleep 500ms Enter
Type "unit deploy -e prod" Sleep 500ms Enter
Sleep 4s

Type "# 3. Run a security audit" Sleep 500ms Enter
Type "unit audit -e prod" Sleep 500ms Enter
Sleep 3s

Type "# Done! Your binary is now an isolated unit." Sleep 1s Enter
Sleep 2s
