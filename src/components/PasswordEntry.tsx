import React, { useState, useRef, useEffect } from 'react';
import { Html5Qrcode } from 'html5-qrcode';
import styled from 'styled-components';
import Card from './common/Card';
import Button from './common/Button';
import Input from './common/Input';

interface PasswordEntryProps {
  routerIp: string;
  password: string;
  setPassword: (password: string) => void;
  onSubmit: () => void;
  error: string | null;
  onBack: () => void;
}

const PasswordForm = styled.form`
  margin-top: 1rem;
`;

const QrScannerContainer = styled.div`
  margin-top: 1.5rem;
  padding: 1rem;
  border: 1px solid ${props => props.theme.colors.border};
  border-radius: ${props => props.theme.radii.md};
`;

const ScannerTitle = styled.h3`
  font-size: ${props => props.theme.fontSizes.md};
  margin-bottom: 0.75rem;
  font-weight: ${props => props.theme.fontWeights.medium};
`;

const QrContainer = styled.div`
  width: 100%;
  max-width: 300px;
  height: 300px;
  margin: 0 auto;
  position: relative;
  overflow: hidden;
`;

const OrDivider = styled.div`
  display: flex;
  align-items: center;
  text-align: center;
  margin: 1.5rem 0;
  
  &::before,
  &::after {
    content: '';
    flex: 1;
    border-bottom: 1px solid ${props => props.theme.colors.border};
  }
  
  &::before {
    margin-right: 1rem;
  }
  
  &::after {
    margin-left: 1rem;
  }
`;

const ButtonGroup = styled.div`
  display: flex;
  gap: 1rem;
  margin-top: 1.5rem;
`;

const InfoText = styled.p`
  color: ${props => props.theme.colors.textSecondary};
  font-size: ${props => props.theme.fontSizes.md};
  margin-bottom: 1.5rem;
`;

const ErrorMessage = styled.div`
  color: ${props => props.theme.colors.error};
  background-color: ${props => props.theme.colors.primaryLight};
  padding: 1rem;
  border-radius: ${props => props.theme.radii.md};
  margin-bottom: 1rem;
`;

const RouterInfo = styled.div`
  display: flex;
  align-items: center;
  padding: 0.75rem;
  background-color: ${props => props.theme.colors.backgroundAlt};
  border-radius: ${props => props.theme.radii.md};
  margin-bottom: 1.5rem;
`;

const RouterIP = styled.div`
  font-weight: ${props => props.theme.fontWeights.medium};
  font-size: ${props => props.theme.fontSizes.md};
`;

const PasswordEntry: React.FC<PasswordEntryProps> = ({
  routerIp,
  password,
  setPassword,
  onSubmit,
  error,
  onBack
}) => {
  const [isSubmitting, setIsSubmitting] = useState(false);
  const [showQrScanner, setShowQrScanner] = useState(false);
  const qrScannerRef = useRef<Html5Qrcode | null>(null);
  const qrContainerRef = useRef<HTMLDivElement>(null);
  
  // Initialize QR scanner when showing the scanner
  useEffect(() => {
    if (showQrScanner && qrContainerRef.current) {
      const qrScannerId = "qr-scanner-container";
      
      // Create container if it doesn't exist
      if (!document.getElementById(qrScannerId)) {
        const container = document.createElement("div");
        container.id = qrScannerId;
        qrContainerRef.current.appendChild(container);
      }
      
      // Initialize the scanner
      qrScannerRef.current = new Html5Qrcode(qrScannerId);
      
      // Start scanning
      qrScannerRef.current.start(
        { facingMode: "environment" },
        {
          fps: 10,
          qrbox: 250
        },
        (decodedText) => {
          // On success - set the password
          setPassword(decodedText);
          stopQrScanner();
          // Auto-submit after scanning
          handleSubmit();
        },
        (errorMessage) => {
          // Ignoring errors in scanning - they're expected during the scan process
          console.log(errorMessage);
        }
      )
      .catch(err => {
        console.error("Error starting QR scanner:", err);
      });
    }
    
    // Cleanup
    return () => {
      stopQrScanner();
    };
  }, [showQrScanner]);
  
  // Stop QR scanner
  const stopQrScanner = () => {
    if (qrScannerRef.current && qrScannerRef.current.isScanning) {
      qrScannerRef.current.stop()
        .then(() => {
          setShowQrScanner(false);
        })
        .catch(err => {
          console.error("Error stopping QR scanner:", err);
        });
    }
  };
  
  // Toggle QR scanner
  const toggleQrScanner = () => {
    setShowQrScanner(!showQrScanner);
  };
  
  const handleSubmit = (e?: React.FormEvent) => {
    if (e) e.preventDefault();
    setIsSubmitting(true);
    onSubmit();
    // In a real implementation, we would wait for the response before setting isSubmitting to false
    // For now, we'll use a timeout to simulate the delay
    setTimeout(() => {
      setIsSubmitting(false);
    }, 2000);
  };
  
  const footerButtons = (
    <ButtonGroup>
      <Button variant="outline" onClick={onBack} disabled={isSubmitting}>
        Back
      </Button>
      <Button
        variant="primary"
        type="submit"
        onClick={() => handleSubmit()}
        isLoading={isSubmitting}
      >
        Connect
      </Button>
    </ButtonGroup>
  );
  
  return (
    <Card
      title="Router Password"
      subtitle="Enter the password for your router"
      footer={footerButtons}
    >
      {error && <ErrorMessage>{error}</ErrorMessage>}
      
      <RouterInfo>
        <RouterIP>IP Address: {routerIp}</RouterIP>
      </RouterInfo>
      
      <InfoText>
        Enter the root password for your router. This is typically the admin password
        you use to access the router's settings page.
      </InfoText>
      
      {!showQrScanner ? (
        <>
          <PasswordForm onSubmit={(e) => handleSubmit(e)}>
            <Input
              label="Password"
              type="password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              fullWidth
              autoFocus
              placeholder="Enter router password"
              disabled={isSubmitting}
            />
          </PasswordForm>
          
          <OrDivider>OR</OrDivider>
          
          <Button
            variant="outline"
            fullWidth
            onClick={toggleQrScanner}
            disabled={isSubmitting}
          >
            Scan Router Password QR Code
          </Button>
        </>
      ) : (
        <QrScannerContainer>
          <ScannerTitle>Scan Router Password QR Code</ScannerTitle>
          <QrContainer ref={qrContainerRef} />
          <Button
            variant="outline"
            fullWidth
            onClick={stopQrScanner}
            style={{ marginTop: '1rem' }}
          >
            Cancel Scanning
          </Button>
        </QrScannerContainer>
      )}
    </Card>
  );
};

export default PasswordEntry;