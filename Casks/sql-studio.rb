cask "sql-studio" do
  version "0.2.6"
  sha256 "6b1975deeaa6b865d637eb7e11c500cfb7cb83ae05fb1410174a71fdec03b266"

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
