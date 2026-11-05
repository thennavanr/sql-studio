cask "sql-studio" do
  version "0.2.3"
  sha256 "e73fb101b606031726c3fa18e256654098975bbb9fc06ffd3f30594698e9119c"

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
