package ai.alertd.imagethat

import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import java.io.File
import mobile.Mobile
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith

@RunWith(AndroidJUnit4::class)
class VaultcoreSmokeTest {
    @Test
    fun createAndReadBack() {
        val cache = InstrumentationRegistry.getInstrumentation().targetContext.cacheDir
        // Go does not see the Android environment; without this it uses /data/local/tmp.
        Mobile.setTempDir(cache.absolutePath)
        val dir = File(cache, "smoke-vault-${System.nanoTime()}").apply { mkdirs() }
        try {
            Mobile.initLocal(dir.absolutePath, "pw-123456")
            val vault = Mobile.openLocal(dir.absolutePath, "pw-123456")
            try {
                val data = ByteArray(10_000) { (it % 251).toByte() }
                val w = vault.create("/hello.bin")
                w.write(data)
                w.close()

                assertEquals(data.size.toLong(), vault.size("/hello.bin"))
                assertArrayEquals(data.copyOfRange(100, 164), vault.readRange("/hello.bin", 100, 64))
                assertTrue(vault.list("/").contains("hello.bin"))
            } finally {
                vault.close()
            }
        } finally {
            dir.deleteRecursively()
        }
    }
}
