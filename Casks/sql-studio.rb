cask "sql-studio" do
  version "0.2.0"
  sha256 "0ee2451936437fc8b4ae6fbd80b8f26daa5171366c451bb24d468143e88f62ae"

  url "https://github.com/thennavanr/sql-studio/releases/download/v#{version}/SQL-Studio-macOS-universal.dmg"
  name "SQL Studio"
  desc "Lightweight SQL Server and Azure SQL client"
  homepage "https://github.com/thennavanr/sql-studio"

  livecheck do
    url :url
    strategy :github_latest
  end

  depends_on macos: ">= :big_sur"

  app "SQL Studio.app"

  # The app is ad-hoc signed but not notarized. Clear the download quarantine
  # so macOS does not report it as "damaged".
  postflight do
    system_command "/usr/bin/xattr",
                   args: ["-dr", "com.apple.quarantine", "#{appdir}/SQL Studio.app"]
  end

  zap trash: "~/Library/Application Support/SQL Studio"
end
