package ai.alertd.imagethat.ui

import org.junit.Assert.assertEquals
import org.junit.Test

class HomeHelpersTest {
    @Test fun summaryCountsServersAndUnlockedVaults() {
        assertEquals("0 servers · all locked", homeSummary(0, 0))
        assertEquals("1 server · 1 vault unlocked", homeSummary(1, 1))
        assertEquals("2 servers · 3 vaults unlocked", homeSummary(2, 3))
    }

    @Test fun serverSubtitleShowsPortAndScheme() {
        assertEquals("Port 8347 · https", serverSubtitle("https://nas.local:8347/"))
        assertEquals("Port 80 · http, not encrypted", serverSubtitle("http://10.0.0.2/"))
        assertEquals("not a url", serverSubtitle("not a url"))
    }

    @Test fun unlockFootnoteUsesTheIdleTimeout() {
        assertEquals("Decrypted only in memory. Locks again after 1 minute without a touch.", unlockFootnote(1))
        assertEquals("Decrypted only in memory. Locks again after 5 minutes without a touch.", unlockFootnote(5))
    }
}
