package ai.alertd.imagethat

import java.io.File
import javax.xml.parsers.DocumentBuilderFactory
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import org.w3c.dom.Document
import org.w3c.dom.Element

class ManifestHardeningTest {
    private val androidNs = "http://schemas.android.com/apk/res/android"

    private val manifest: Document by lazy {
        val path = System.getProperty("merged.manifest")
        requireNotNull(path) { "merged.manifest system property not set (run via Gradle)" }
        val factory = DocumentBuilderFactory.newInstance().apply { isNamespaceAware = true }
        factory.newDocumentBuilder().parse(File(path))
    }

    private val application: Element
        get() = manifest.getElementsByTagName("application").item(0) as Element

    @Test
    fun backupIsDisabled() {
        assertEquals("false", application.getAttributeNS(androidNs, "allowBackup"))
    }

    @Test
    fun dataExtractionRulesAreSet() {
        assertTrue(application.hasAttributeNS(androidNs, "dataExtractionRules"))
        assertEquals("@xml/data_extraction_rules", application.getAttributeNS(androidNs, "dataExtractionRules"))
    }

    @Test
    fun noContentProviders() {
        assertEquals(0, manifest.getElementsByTagName("provider").length)
    }

    @Test
    fun onlyExpectedPermissions() {
        val nodes = manifest.getElementsByTagName("uses-permission")
        val names = (0 until nodes.length)
            .map { (nodes.item(it) as Element).getAttributeNS(androidNs, "name") }
            // AndroidX adds an app-private, signature-level permission for dynamic receivers.
            .filterNot { it.endsWith(".DYNAMIC_RECEIVER_NOT_EXPORTED_PERMISSION") }
            .toSet()
        assertEquals(
            setOf("android.permission.INTERNET", "android.permission.USE_BIOMETRIC"),
            names,
        )
    }
}
