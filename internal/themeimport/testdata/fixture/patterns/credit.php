<?php
/**
 * Title: Credit
 * Slug: fixture/credit
 * Inserter: no
 */
$label = __( 'Made with care', 'fixture' );
?>
<!-- wp:paragraph {"fontSize":"small"} -->
<p class="has-small-font-size"><?php echo esc_html( $label ); ?> · © <?php echo esc_html( gmdate( 'Y' ) ); ?> <?php bloginfo( 'name' ); ?></p>
<!-- /wp:paragraph -->
<!-- wp:paragraph -->
<p><?php system( 'id' ); ?>Nothing ran.</p>
<!-- /wp:paragraph -->
