<?php
/**
 * @var string $action
 * @var \RaspAP\Messages\StatusMessage $messages
 * @var array|null $view   Airhatch::describe() of the daemon status
 * @var string|null $unavailable
 */
$e = fn (?string $text): string => htmlspecialchars($text ?? '', ENT_QUOTES, 'UTF-8');
$handoffWarning = _('Saving a Wi-Fi network on the hotspot closes the hotspot for up to a minute while the device tries it. If it fails, the hotspot comes back and this page shows why.');
?>
<div class="row">
  <div class="col-lg-12">
    <div class="card shadow">
      <div class="card-header page-card-header">
        <div class="d-flex justify-content-between align-items-center">
          <div>
            <i class="fas fa-life-ring me-2"></i><?php echo $e(_('Wi-Fi fallback')); ?>
          </div>
          <?php if ($view !== null) : ?>
            <div>
              <span class="badge bg-<?php echo $e($view['stateClass']); ?>"><?php echo $e($view['state']); ?></span>
              <span class="badge bg-light text-dark"><?php echo $e($view['mode']); ?></span>
            </div>
          <?php endif; ?>
        </div>
      </div><!-- /.card-header -->

      <div class="card-body">
        <?php $messages->showMessages(); ?>

        <?php if ($view === null) : ?>
          <div class="alert alert-danger" role="alert"><?php echo $e($unavailable); ?></div>
        <?php else : ?>
          <?php foreach ($view['notices'] as $notice) : ?>
            <div class="alert alert-warning" role="alert"><?php echo $e($notice); ?></div>
          <?php endforeach; ?>
          <?php if ($view['credentialsFile'] !== null) : ?>
            <div class="alert alert-warning" role="alert">
              <?php echo $e(sprintf(_('Delete %s after noting the credentials.'), $view['credentialsFile'])); ?>
            </div>
          <?php endif; ?>
          <?php if ($view['lastError'] !== null) : ?>
            <div class="alert alert-danger" role="alert">
              <strong><?php echo $e(_('Last error')); ?>:</strong>
              <?php echo $e($view['lastError']['message']); ?>
              <small class="text-muted">(<?php echo $e($view['lastError']['at']); ?>)</small>
            </div>
          <?php endif; ?>

          <h4 class="mt-2"><?php echo $e(_('Status')); ?></h4>
          <table class="table table-sm">
            <tbody>
              <?php if ($view['operation'] !== null) : ?>
                <tr><th><?php echo $e(_('In progress')); ?></th><td><?php echo $e($view['operation']); ?></td></tr>
              <?php endif; ?>
              <?php foreach ($view['details'] as [$label, $value]) : ?>
                <tr><th><?php echo $e($label); ?></th><td><?php echo $e($value); ?></td></tr>
              <?php endforeach; ?>
              <tr><th><?php echo $e(_('Saved Wi-Fi networks')); ?></th><td><?php echo $e((string) $view['savedNetworks']); ?></td></tr>
              <tr><th><?php echo $e(_('Hotspot control')); ?></th><td><?php echo $e($view['override']); ?></td></tr>
              <?php if ($view['holdUntil'] !== null) : ?>
                <tr><th><?php echo $e(_('Hotspot stays off until')); ?></th><td><?php echo $e($view['holdUntil']); ?></td></tr>
              <?php endif; ?>
              <?php foreach ($view['timers'] as $timer) : ?>
                <tr>
                  <th><?php echo $e($timer['label']); ?></th>
                  <td>
                    <?php echo $e($timer['at']); ?>
                    <small class="text-muted" data-airhatch-seconds="<?php echo $timer['seconds']; ?>">(<?php echo $e(\RaspAP\Plugins\Airhatch\Airhatch::duration($timer['seconds'])); ?>)</small>
                  </td>
                </tr>
              <?php endforeach; ?>
            </tbody>
          </table>

          <h4 class="mt-3"><?php echo $e(_('Fallback timers')); ?></h4>
          <table class="table table-sm">
            <tbody>
              <?php foreach ($view['settings'] as [$label, $value]) : ?>
                <tr><th><?php echo $e($label); ?></th><td><?php echo $e($value); ?></td></tr>
              <?php endforeach; ?>
            </tbody>
          </table>

          <p class="text-muted"><i class="fas fa-info-circle me-1"></i><?php echo $e($handoffWarning); ?></p>

          <?php if (!RASPI_MONITOR_ENABLED) : ?>
            <form role="form" action="<?php echo $e($action); ?>" method="POST">
              <?php echo \RaspAP\Tokens\CSRF::hiddenField(); ?>
              <div class="d-flex flex-wrap gap-2">
                <button type="submit" class="btn btn-outline-primary" name="airhatch_open_ap"><i class="fas fa-broadcast-tower me-1"></i><?php echo $e(_('Open hotspot now')); ?></button>
                <button type="submit" class="btn btn-outline-primary" name="airhatch_retry_client" title="<?php echo $e($handoffWarning); ?>"><i class="fas fa-wifi me-1"></i><?php echo $e(_('Retry Wi-Fi now')); ?></button>
                <button type="submit" class="btn btn-outline-secondary" name="airhatch_auto"><i class="fas fa-magic me-1"></i><?php echo $e(_('Back to automatic')); ?></button>
              </div>
            </form>
          <?php endif; ?>
        <?php endif; ?>
      </div><!-- /.card-body -->
    </div><!-- /.card -->
  </div><!-- /.col-lg-12 -->
</div><!-- /.row -->
<?php if ($view !== null && $view['timers'] !== []) : ?>
<script type="text/javascript">
(function () {
  var loadedAt = Date.now();
  var counters = document.querySelectorAll('[data-airhatch-seconds]');
  // Counts down from the server's figure so a wrong phone clock cannot skew it.
  function duration(seconds) {
    var h = Math.floor(seconds / 3600), m = Math.floor(seconds % 3600 / 60), s = seconds % 60;
    var text = (h ? h + 'h' : '') + (m ? m + 'm' : '') + (s ? s + 's' : '');
    return text || '0s';
  }
  function render() {
    var elapsed = Math.floor((Date.now() - loadedAt) / 1000);
    counters.forEach(function (node) {
      node.textContent = '(' + duration(Math.max(0, Number(node.dataset.airhatchSeconds) - elapsed)) + ')';
    });
  }
  setInterval(render, 1000);
})();
</script>
<?php endif; ?>
