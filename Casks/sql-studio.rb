cask "sql-studio" do
  version "0.2.5"
  sha256 "7a4ae9a2af9d367ec1c9b83616f1b7f83b5e9ee7abf5694cc25ab097bc2c356c"

  url "https://github.com/thennavanr/sql-studio/releases/download/v#{version}/SQL-Studio-macOS-universal.dmg"
  name "SQL Studio"
  desc "Lightweight SQL Server, Azure SQL and MongoDB client"
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
